package worker

// SmokeCommand verifies only that the Adapter CLI exists in the Runtime Image.
// Provider/API availability is checked separately with per-Run credentials.
func SmokeCommand(adapter string) ([]string, error) {
	definition, err := LookupAdapterDefinition(adapter)
	if err != nil {
		return nil, err
	}
	return definition.SmokeCommand(), nil
}
