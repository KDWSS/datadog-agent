package hostname

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func dummyProvider(ctx context.Context, options map[string]interface{}) (string, error) {
	return "dummy-hostname", nil
}

func dummyErrorProvider(ctx context.Context, options map[string]interface{}) (string, error) {
	return "", fmt.Errorf("Some error")
}

func dummyInvalideProvider(ctx context.Context, options map[string]interface{}) (string, error) {
	return "some invalid hostname", nil
}

func TestRegisterHostnameProvider(t *testing.T) {
	RegisterHostnameProvider("dummy", dummyProvider)
	assert.Contains(t, providerCatalog, "dummy")
	delete(providerCatalog, "dummy")
}

func TestGetProvider(t *testing.T) {
	RegisterHostnameProvider("dummy", dummyProvider)
	defer delete(providerCatalog, "dummy")
	assert.NotNil(t, GetProvider("dummy"))
	assert.Nil(t, GetProvider("does not exists"))
}

func TestGetHostname(t *testing.T) {
	RegisterHostnameProvider("dummy", dummyProvider)
	defer delete(providerCatalog, "dummy")

	name, err := GetHostname("dummy", context.Background(), nil)
	assert.NoError(t, err)
	assert.Equal(t, "dummy-hostname", name)
}

func TestGetHostnameUnknown(t *testing.T) {
	_, err := GetHostname("dummy", context.Background(), nil)
	assert.Error(t, err)
}

func TestGetHostnameError(t *testing.T) {
	RegisterHostnameProvider("dummy", dummyErrorProvider)
	defer delete(providerCatalog, "dummy")

	_, err := GetHostname("dummy", context.Background(), nil)
	assert.Error(t, err)
}

func TestGetHostnameInvalid(t *testing.T) {
	RegisterHostnameProvider("dummy", dummyInvalideProvider)
	defer delete(providerCatalog, "dummy")

	_, err := GetHostname("dummy", context.Background(), nil)
	assert.Error(t, err)
}
