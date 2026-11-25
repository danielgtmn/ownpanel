//go:build !unix

package backup

func giveBack(string) error { return nil }
