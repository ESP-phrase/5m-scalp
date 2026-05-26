package strategy

type Registry struct {
	strategies map[string]Strategy
	active     Strategy
}

func NewRegistry() *Registry {
	return &Registry{
		strategies: make(map[string]Strategy),
	}
}

func (r *Registry) Register(s Strategy) {
	r.strategies[s.Name()] = s
}

func (r *Registry) SetActive(name string) bool {
	s, ok := r.strategies[name]
	if !ok {
		return false
	}

	if r.active != nil {
		r.active.Reset()
	}

	r.active = s
	return true
}

func (r *Registry) Active() Strategy {
	return r.active
}

func (r *Registry) List() []Strategy {
	all := make([]Strategy, 0, len(r.strategies))
	for _, s := range r.strategies {
		all = append(all, s)
	}
	return all
}
