// Package workspace names the access unit of ADR 0050 (section 3): every row of user data belongs to exactly one workspace,
// and every store method that reads or writes such a row takes the workspace as an ID, never a bare integer, so a dataset id
// cannot be passed where a workspace is meant and a query cannot drop the scope without a compile error or a failing guard
// (internal/arch TestEveryScopedQueryNamesTheWorkspace). Local mode has one workspace, Local.
//
// It imports nothing, so every store may depend on it.
package workspace

import "strconv"

// ID is a workspace's id.
type ID int64

// Local is the single implicit workspace of local mode, and the workspace every row written before schema 31 belongs to.
const Local ID = 1

func (w ID) String() string { return strconv.FormatInt(int64(w), 10) }
