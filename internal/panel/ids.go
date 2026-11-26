package panel

import "strconv"

// Ids are strings at the panel's edge (API, MCP, web) and integers in the
// store underneath. The store will move to a library whose ids are always
// strings; records that exist today keep their number as text ("42").
// These two helpers are the only place that converts between the two.

// idString is the public form of a store id.
func idString(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// storeID turns a public id into the store's key. Anything that is not the
// canonical text of a positive number cannot name a record, so it answers
// NotFound like an id that does not exist ("<what> not found").
func storeID(id, what string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
		return 0, errorf(NotFound, "%s not found", what)
	}
	return n, nil
}
