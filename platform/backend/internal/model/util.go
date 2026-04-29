package model

import "log"

// SaveOrLog wraps DB.Save and logs failures with a short context tag,
// preserving the legacy "fire-and-forget save" behaviour while making
// errors observable. Use it at sites where:
//   - the operation is non-fatal to the request (e.g. heartbeat
//     refresh, lock release, cleanup loops),
//   - aborting on save failure would surprise the caller more than a
//     dropped write,
//   - but a silent ignore would mean DB outages corrupt state without
//     any signal.
//
// For request-level paths where the user must see a 5xx on save
// failure, keep using `if err := DB.Save(&x).Error; err != nil { ... }`
// and return the error explicitly.
//
// Naming: SaveOrLog (not LogSaveErr) makes the call site read
// imperatively — `model.SaveOrLog(&task, "claim/release task")`.
func SaveOrLog(rec interface{}, what string) {
	if err := DB.Save(rec).Error; err != nil {
		log.Printf("[DB] save %s: %v", what, err)
	}
}
