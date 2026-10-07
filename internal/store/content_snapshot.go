package store

// ContentSnapshot is one fully collected Ed course. Kinds names the complete
// remote inventories; unavailable optional inventories must not be listed.
// Files are staged and published by the caller before the database transaction.
type ContentSnapshot struct {
	Items     []Item
	Files     []File
	Grades    []Grade
	Deadlines []Deadline
	Kinds     []string
	Warnings  []string
}

// SnapshotResult counts persisted content and detected events for one course.
type SnapshotResult struct {
	Items, Files, Changes int
}
