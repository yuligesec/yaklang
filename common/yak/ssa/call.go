package ssa

func NewCall(target Value, args, binding []Value, block *BasicBlock) *Call {
	c := &Call{
		anInstruction: NewInstruction(),
		anValue:       NewValue(),
		Method:        target,
		Args:          args,
		binding:       binding,
		Async:         false,
		Unpack:        false,
		IsDropError:   false,
		IsEllipsis:    false,
	}
	return c
}

func (f *FunctionBuilder) NewCall(target Value, args []Value) *Call {
	return NewCall(target, args, nil, f.CurrentBlock)
}

func (c *Call) HandleFreeValue(fvs []string, sideEffect []string) {
	builder := c.GetFunc().builder
	recoverBuilder := builder.SetCurrent(c)
	defer recoverBuilder()

	// parent := builder.parentBuilder

	for _, name := range fvs {
		// get current function variable for call this closure
		v := builder.ReadVariableBefore(name, false, c)
		// if not get, try build freeValue in parent function.
		if v == nil && builder.CanBuildFreeValue(name) {
			v = builder.BuildFreeValue(name)
		}
		if v != nil {
			c.binding = append(c.binding, v)
		} else {
			c.NewError(Error, SSATAG, BindingNotFound(name))
		}
	}

	for _, name := range sideEffect {
		v := builder.ReadVariableBefore(name, false, c)
		if v == nil {
			// if side effect not found, just skip
			continue
		}
		// handle side effect
		sideEffect := NewSideEffect(name, c)
		builder.EmitInstructionAfter(sideEffect, c)
		sideEffect.SetPosition(c.GetPosition())
		sideEffect.SetType(BasicTypes[Any])
		builder.WriteVariable(name, sideEffect)
		InsertValueReplaceOriginal(v, sideEffect)
	}

}
