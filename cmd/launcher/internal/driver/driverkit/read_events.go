package driverkit

// scanEvents decodes the log at path with a fresh decoder and calls fn with
// every event, in order.
func scanEvents(path string, newDecoder func() Decoder, fn func(Event)) error {
	l, err := DecodeLog(path, newDecoder)
	if err != nil {
		return err
	}
	l.Each(fn)
	return nil
}
