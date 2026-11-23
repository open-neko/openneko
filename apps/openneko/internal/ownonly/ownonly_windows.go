package ownonly

import "github.com/open-neko/openneko/apps/ax-harness/ownacl"

// Set gives path a protected access list that allows only the current user
// and SYSTEM. A folder passes the list to its files and subfolders.
func Set(path string) error { return ownacl.Set(path) }

// Verify returns an error unless the access list of path allows only the
// current user and SYSTEM.
func Verify(path string) error { return ownacl.Verify(path) }
