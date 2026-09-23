package routing

type Rule struct {
	Name     string
	Prefix   string
	Priority int
	Enabled  bool
}

// Choose returns the most specific enabled rule that matches path.
func Choose(rules []Rule, path string) (Rule, bool) {
	return Rule{}, false // TODO: implement the documented routing precedence.
}
