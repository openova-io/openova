package huawei

// pageLimitForTest lowers the list page size so pagination is exercised with
// tiny fixtures; the returned func restores the default.
func pageLimitForTest(n int) func() {
	old := pageLimit
	pageLimit = n
	return func() { pageLimit = old }
}
