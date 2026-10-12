package run

import "testing"

func newTestExecutionBinding(
	t *testing.T,
	runtime Runtime,
	secrets SecretEnvironmentProvider,
	handles ExecutionHandleStore,
	continuation RunContinuationAuthorizer,
	recovery ...ProcessRecoveryAuthorizer,
) ExecutionBinding {
	t.Helper()
	if continuation == nil && len(recovery) > 0 {
		if authorizer, ok := recovery[0].(RunContinuationAuthorizer); ok {
			continuation = authorizer
		}
	}
	binding, err := NewExecutionBinding(runtime, secrets, handles, continuation, recovery...)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}
