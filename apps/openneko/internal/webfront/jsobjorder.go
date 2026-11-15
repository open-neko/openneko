package webfront

func (o *jsObj) ObjectKeys() []string { return o.ordered() }

func (o *jsObj) ObjectValue(key string) any { return o.vals[key] }
