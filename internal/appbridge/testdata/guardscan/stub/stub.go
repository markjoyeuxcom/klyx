package fixture

import "context"

// The guarded shell's names, for the fixtures beside this directory: each
// fixture is type-checked with this file as package fixture.

type Ticket struct{}

func (Ticket) Write(ctx context.Context) context.Context { return ctx }
func (Ticket) StartCLI() error                           { return nil }

func guardFor(string) (Ticket, error) { return Ticket{}, nil }
func write(context.Context) error     { return nil }
func check(context.Context) error     { return nil }
func pick() context.Context           { return context.Background() }
func prepare()                        {}

func fenced(ctx context.Context, fence func() error) (context.Context, context.CancelFunc, func() string) {
	ctx, cancel := context.WithCancel(ctx)
	return ctx, cancel, func() string { return "" }
}
