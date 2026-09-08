// SPDX-License-Identifier: MIT

package main

// BannedEntities lists entity ids (org id or user id) denied the runner
// service. Kept in code so a ban ships through review, with the why in each
// entry's comment. Resolve an id with `gh api orgs/<name> --jq '.id'`.
var BannedEntities = []int64{
	325883364, // github.com/demo99969
	325940811, // github.com/myjobs99
}
