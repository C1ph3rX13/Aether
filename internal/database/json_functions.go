package database

import (
	"database/sql/driver"

	"github.com/C1ph3rX13/Aether/internal/spec"
	"modernc.org/sqlite"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(
		"aether_json_no_duplicate_keys", 1,
		func(_ *sqlite.FunctionContext, arguments []driver.Value) (driver.Value, error) {
			data, ok := sqliteText(arguments, 0)
			if !ok || spec.ValidateNoDuplicateFields(data) != nil {
				return int64(0), nil
			}
			return int64(1), nil
		},
	)
	sqlite.MustRegisterDeterministicScalarFunction(
		"aether_execution_spec_valid", 4,
		func(_ *sqlite.FunctionContext, arguments []driver.Value) (driver.Value, error) {
			schema, schemaOK := sqliteString(arguments, 0)
			data, dataOK := sqliteText(arguments, 1)
			task, taskOK := sqliteString(arguments, 2)
			target, targetOK := sqliteOptionalString(arguments, 3)
			if !schemaOK || !dataOK || !taskOK || !targetOK {
				return int64(0), nil
			}
			if _, err := spec.ValidateAndProject(schema, data, task, target); err != nil {
				return int64(0), nil
			}
			return int64(1), nil
		},
	)
	sqlite.MustRegisterDeterministicScalarFunction(
		"aether_execution_spec_matches", 11,
		func(_ *sqlite.FunctionContext, arguments []driver.Value) (driver.Value, error) {
			schema, schemaOK := sqliteString(arguments, 0)
			data, dataOK := sqliteText(arguments, 1)
			task, taskOK := sqliteString(arguments, 2)
			target, targetOK := sqliteOptionalString(arguments, 3)
			generation, generationOK := sqliteInt64(arguments, 4)
			revision, revisionOK := sqliteInt64(arguments, 5)
			inputHash, inputOK := sqliteString(arguments, 6)
			worker, workerOK := sqliteString(arguments, 7)
			config, configOK := sqliteString(arguments, 8)
			image, imageOK := sqliteString(arguments, 9)
			result, resultOK := sqliteString(arguments, 10)
			if !schemaOK || !dataOK || !taskOK || !targetOK || !generationOK || !revisionOK ||
				!inputOK || !workerOK || !configOK || !imageOK || !resultOK {
				return int64(0), nil
			}
			projection, err := spec.ValidateAndProject(schema, data, task, target)
			if err != nil || int64(projection.InputGeneration) != generation || projection.InputRevision != revision ||
				projection.InputEvidence.SHA256 != inputHash || projection.WorkerName != worker ||
				projection.ConfigRevision != config || projection.ImageReference != image ||
				projection.ResultSchema != result {
				return int64(0), nil
			}
			return int64(1), nil
		},
	)
}

func sqliteText(arguments []driver.Value, index int) ([]byte, bool) {
	if index >= len(arguments) {
		return nil, false
	}
	switch value := arguments[index].(type) {
	case string:
		return []byte(value), true
	case []byte:
		return value, true
	default:
		return nil, false
	}
}

func sqliteString(arguments []driver.Value, index int) (string, bool) {
	data, ok := sqliteText(arguments, index)
	return string(data), ok
}

func sqliteOptionalString(arguments []driver.Value, index int) (*string, bool) {
	if index >= len(arguments) {
		return nil, false
	}
	if arguments[index] == nil {
		return nil, true
	}
	value, ok := sqliteString(arguments, index)
	return &value, ok
}

func sqliteInt64(arguments []driver.Value, index int) (int64, bool) {
	if index >= len(arguments) {
		return 0, false
	}
	value, ok := arguments[index].(int64)
	return value, ok
}
