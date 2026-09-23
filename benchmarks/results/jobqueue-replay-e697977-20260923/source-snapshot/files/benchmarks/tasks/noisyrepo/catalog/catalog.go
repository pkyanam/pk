package catalog

// Source maps a request path to one enabled handler.
type Source struct {
	Name     string
	Prefix   string
	Priority int
	Enabled  bool
}

// Select chooses the best enabled source for path. TODO: implement the
// documented specificity, priority, boundary, and stable-tie rules.
func Select(sources []Source, path string) (Source, bool) {
	return Source{}, false
}
