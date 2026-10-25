package fixture

// a ticket taken and never used: the write runs unfenced
func scale(c string) error {
	t, err := guardFor(c)
	if err != nil {
		return err
	}
	_ = t
	return write(nil)
}
