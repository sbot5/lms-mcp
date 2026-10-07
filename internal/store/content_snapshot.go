package store

// ContentSnapshot is one fully collected Ed course. Kinds names the complete
// remote inventories; unavailable optional inventories must not be listed.
// Files are staged and published by the caller before the database transaction.
type ContentSnapshot struct {
	Items          []Item
	Files          []File
	Grades         []Grade
	Deadlines      []Deadline
	Kinds          []string
	Warnings       []string
	LeaseHolder    string                      // when set, the transaction must still own the sync lease
	DeadlineFields map[string]DeadlinePresence // item ID -> known window/status fields
}

// DeadlinePresence distinguishes an explicit zero from an unavailable field.
type DeadlinePresence struct {
	Opens, Due, Cutoff, Status bool
}

// SnapshotResult counts persisted content and detected events for one course.
type SnapshotResult struct {
	Items, Files, Changes int
}
