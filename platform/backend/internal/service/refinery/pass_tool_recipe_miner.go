package refinery

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/a3c/platform/internal/model"
)

// ToolRecipeMiner groups successful Episodes by task tag and extracts a
// representative tool-call "recipe" for each (tag, file_category) bucket.
// A recipe is the most common tool sequence that led to success for that
// kind of task — useful as an explicit step-by-step hint for agents
// working on similar future tasks.
//
// This is a heavier synthesis than PatternExtractor: instead of surfacing
// every high-confidence n-gram, it picks one canonical recipe per bucket
// so the agent prompt stays short.
type ToolRecipeMiner struct{}

func (ToolRecipeMiner) Name() string       { return "tool_recipe_miner/v1" }
func (ToolRecipeMiner) Produces() []string { return []string{"tool_recipe"} }
func (ToolRecipeMiner) Requires() []string { return []string{"episode"} }

const (
	recipeMinSupport = 3 // need at least 3 successful episodes in a bucket
	recipeTopK       = 5 // keep top-K most common sequences per bucket
)

func (ToolRecipeMiner) Run(ctx *Context) (Stats, error) {
	episodes, err := loadEpisodes(ctx)
	if err != nil {
		return nil, err
	}
	if len(episodes) == 0 {
		return Stats{"episodes_seen": 0, "recipes_upserted": 0}, nil
	}

	// Bucket episodes by (task_tag, file_category). An episode with no tags
	// lands in the "_untagged" bucket. We fetch tags from the TaskTag table
	// lazily in a single pass so we don't round-trip per episode.
	taskTags := map[string][]string{} // taskID → tags
	taskIDs := make([]string, 0, len(episodes))
	for _, ep := range episodes {
		if ep.TaskID != "" {
			taskIDs = append(taskIDs, ep.TaskID)
		}
	}
	if len(taskIDs) > 0 {
		var tags []model.TaskTag
		model.DB.Where("task_id IN ?", taskIDs).Find(&tags)
		for _, t := range tags {
			taskTags[t.TaskID] = append(taskTags[t.TaskID], t.Tag)
		}
	}

	// bucketKey → sequence → count, plus per-sequence source IDs so the
	// recipe we emit can point at episodes that actually used that exact
	// sequence (not an arbitrary sample from the bucket).
	type bucket struct {
		tag           string
		fileCategory  string
		sequences     map[string]int
		seqSourceIDs  map[string][]string
		total         int
	}
	buckets := map[string]*bucket{}

	for _, ep := range episodes {
		if ep.Outcome != "success" {
			continue
		}
		seq := strings.TrimSpace(ep.ToolSequence)
		if seq == "" {
			continue
		}
		fileCategory := inferFileCategory(ep.FilesTouched)
		tagList := taskTags[ep.TaskID]
		if len(tagList) == 0 {
			tagList = []string{"_untagged"}
		}
		for _, tag := range tagList {
			key := tag + "|" + fileCategory
			b, ok := buckets[key]
			if !ok {
				b = &bucket{
					tag:          tag,
					fileCategory: fileCategory,
					sequences:    map[string]int{},
					seqSourceIDs: map[string][]string{},
				}
				buckets[key] = b
			}
			b.sequences[seq]++
			b.total++
			if len(b.seqSourceIDs[seq]) < 20 {
				b.seqSourceIDs[seq] = append(b.seqSourceIDs[seq], ep.ID)
			}
		}
	}

	// Pre-build an episode index so each recipe's topic-context
	// extraction is O(1) per source ID rather than re-scanning the
	// loaded episodes slice every iteration.
	epIndex := make(map[string]*model.Episode, len(episodes))
	for i := range episodes {
		epIndex[episodes[i].ID] = &episodes[i]
	}

	upserted := 0
	for _, b := range buckets {
		if b.total < recipeMinSupport {
			continue
		}

		// Pick top-K most common sequences in this bucket.
		type entry struct {
			seq   string
			count int
		}
		var entries []entry
		for s, c := range b.sequences {
			entries = append(entries, entry{s, c})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].count > entries[j].count })
		if len(entries) > recipeTopK {
			entries = entries[:recipeTopK]
		}

		top := entries[0]
		// Confidence = top sequence's share of the bucket
		confidence := float64(top.count) / float64(b.total)

		steps := strings.Fields(top.seq)
		variants := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			variants = append(variants, map[string]any{
				"tool_sequence": strings.Fields(e.seq),
				"count":         e.count,
				"share":         float64(e.count) / float64(b.total),
			})
		}
		payload, _ := json.Marshal(map[string]any{
			"task_tag":      b.tag,
			"file_category": b.fileCategory,
			"steps":         steps,
			"support":       b.total,
			"confidence":    confidence,
			"variants":      variants,
		})

		name := fmt.Sprintf("recipe[%s/%s]: %s", b.tag, b.fileCategory, strings.Join(steps, "→"))
		summary := fmt.Sprintf("Recipe for tag=%s on %s files: %s (used in %d/%d successful episodes, %.0f%%)",
			b.tag, b.fileCategory, strings.Join(steps, " → "), top.count, b.total, confidence*100)
		// Topic context pulls in concrete file names and task titles from
		// the episodes that actually used the winning sequence — so a
		// "grep→read→edit" recipe in the auth domain reads differently
		// from the same sequence in the i18n domain, and bge picks up
		// on that distinction.
		summary += extractTopicContext(topicSources{
			episodes: resolveEpisodes(b.seqSourceIDs[top.seq], epIndex),
		})

		ka := &model.KnowledgeArtifact{
			ProjectID:    ctx.ProjectID,
			Kind:         "tool_recipe",
			Name:         name,
			Summary:      summary,
			Payload:      string(payload),
			ProducedBy:   ToolRecipeMiner{}.Name(),
			SourceEvents: sourceEventsJSON(b.seqSourceIDs[top.seq]),
			Confidence:   confidence,
		}
		if err := upsertArtifact(ka); err != nil {
			continue
		}
		upserted++
	}

	return Stats{
		"episodes_seen":    len(episodes),
		"buckets":          len(buckets),
		"recipes_upserted": upserted,
	}, nil
}
