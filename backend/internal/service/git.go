package service

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
)

func getRepoPath(projectID string) string {
	project, _ := repo.GetProjectByID(projectID)
	if project != nil && project.GithubRepo != "" {
		return filepath.Join("data", "projects", projectID, "repo")
	}
	return filepath.Join("data", "projects", projectID, "repo")
}

func runGit(projectID string, args ...string) (string, error) {
	repoPath := getRepoPath(projectID)
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		return "", fmt.Errorf("repo directory not found: %s", repoPath)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %v failed: %w, output: %s", args, err, string(out))
	}
	return string(out), nil
}

func GitInit(projectID string) error {
	repoPath := getRepoPath(projectID)
	os.MkdirAll(repoPath, 0755)
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); os.IsNotExist(err) {
		cmd := exec.Command("git", "init")
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init failed: %w, output: %s", err, string(out))
		}
		cmd = exec.Command("git", "config", "user.email", "a3c@platform.local")
		cmd.Dir = repoPath
		cmd.Run()
		cmd = exec.Command("git", "config", "user.name", "A3C Platform")
		cmd.Dir = repoPath
		cmd.Run()
		log.Printf("[Git] Initialized repo for project %s", projectID)
	}
	return nil
}

func GitAddAndCommit(projectID string, taskName string, taskDesc string) error {
	repoPath := getRepoPath(projectID)
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		return fmt.Errorf("repo directory not found")
	}

	if _, err := runGit(projectID, "add", "-A"); err != nil {
		return err
	}

	commitMsg := fmt.Sprintf("[task:%s] %s", taskName, taskDesc)
	if out, err := runGit(projectID, "commit", "-m", commitMsg); err != nil {
		if strings.Contains(out, "nothing to commit") {
			log.Printf("[Git] Nothing to commit for project %s", projectID)
			return nil
		}
		return err
	}

	log.Printf("[Git] Committed for project %s: %s", projectID, commitMsg)
	return nil
}

func GitDiff(projectID string, fromVersion string) (string, error) {
	tagName := fromVersion
	out, err := runGit(projectID, "diff", tagName, "--stat")
	if err != nil {
		if strings.Contains(err.Error(), "unknown revision") {
			return "", nil
		}
		return "", err
	}
	return out, nil
}

func GitTagVersion(projectID string, version string) error {
	_, err := runGit(projectID, "tag", version)
	if err != nil {
		return fmt.Errorf("git tag failed: %w", err)
	}
	log.Printf("[Git] Tagged %s for project %s", version, projectID)
	return nil
}

func GitRevertToVersion(projectID string, version string) error {
	_, err := runGit(projectID, "checkout", version)
	if err != nil {
		return fmt.Errorf("git checkout %s failed: %w", version, err)
	}
	_, err = runGit(projectID, "checkout", "-b", "revert-"+version)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			_, err = runGit(projectID, "checkout", "revert-"+version)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	cmd := exec.Command("git", "commit", "-m", "Revert to "+version)
	cmd.Dir = getRepoPath(projectID)
	if out, err := cmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "nothing to commit") {
			return fmt.Errorf("git commit failed: %w", err)
		}
	}

	log.Printf("[Git] Reverted project %s to %s", projectID, version)
	return nil
}

func IncrementVersion(projectID string) (string, error) {
	versionBlock, _ := repo.GetContentBlock(projectID, "version")
	currentVersion := "v1.0"
	if versionBlock != nil {
		currentVersion = versionBlock.Content
	}

	newVersion := incrementVersionString(currentVersion)
	if versionBlock != nil {
		versionBlock.Content = newVersion
		versionBlock.Version++
		model.DB.Save(versionBlock)
	} else {
		vb := model.ContentBlock{
			ID:        model.GenerateID("cb"),
			ProjectID: projectID,
			BlockType: "version",
			Content:   newVersion,
			Version:   1,
		}
		model.DB.Create(&vb)
	}

	GitTagVersion(projectID, newVersion)
	return newVersion, nil
}

func incrementVersionString(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return "v2.1"
	}
	var milestone, task int
	fmt.Sscanf(parts[0], "%d", &milestone)
	fmt.Sscanf(parts[1], "%d", &task)
	task++
	return fmt.Sprintf("v%d.%d", milestone, task)
}

func GitListVersions(projectID string) ([]string, error) {
	out, err := runGit(projectID, "tag", "-l")
	if err != nil {
		return nil, fmt.Errorf("git tag list failed: %w", err)
	}
	tags := strings.Split(strings.TrimSpace(out), "\n")
	var versions []string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" && strings.HasPrefix(t, "v") {
			versions = append(versions, t)
		}
	}
	return versions, nil
}

func SwitchMilestoneVersion(projectID string) (string, error) {
	versionBlock, _ := repo.GetContentBlock(projectID, "version")
	currentVersion := "v1.0"
	if versionBlock != nil {
		currentVersion = versionBlock.Content
	}

	v := strings.TrimPrefix(currentVersion, "v")
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return "v2.0", nil
	}
	var milestone int
	fmt.Sscanf(parts[0], "%d", &milestone)
	milestone++
	newVersion := fmt.Sprintf("v%d.0", milestone)

	if versionBlock != nil {
		versionBlock.Content = newVersion
		versionBlock.Version++
		model.DB.Save(versionBlock)
	} else {
		vb := model.ContentBlock{
			ID:        model.GenerateID("cb"),
			ProjectID: projectID,
			BlockType: "version",
			Content:   newVersion,
			Version:   1,
		}
		model.DB.Create(&vb)
	}

	GitTagVersion(projectID, newVersion)
	return newVersion, nil
}