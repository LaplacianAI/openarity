package harness

// DefaultTokenBudget is what a run may spend when neither its agent nor its
// caller set a budget.
const DefaultTokenBudget = 1_000_000

// cachedShare is the part of a fresh token a cached one counts for: a tenth,
// as providers bill it.
const cachedShare = 10

// Spent is what a run's tokens count against its budget: fresh input and
// output in full, a cached input token as a tenth. The store's run_spent
// counts the same; a test holds the two together. Cached is held within
// input, as the store's runs_cached_within_input holds it.
func Spent(input, cached, output int64) int64 {
	cached = min(max(cached, 0), input)
	return input - cached + cached/cachedShare + output
}

// BudgetOf is a run's budget: its own, or the default.
func BudgetOf(set *int64) int64 {
	if set == nil {
		return DefaultTokenBudget
	}
	return *set
}
