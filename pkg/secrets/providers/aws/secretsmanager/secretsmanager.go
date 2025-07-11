package secretsmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/tidwall/gjson"

	v1 "kusionstack.io/kusion/pkg/apis/api.kusion.io/v1"
	"kusionstack.io/kusion/pkg/secrets"
	"kusionstack.io/kusion/pkg/secrets/providers/aws/auth"
)

const (
	errMissingProviderSpec   = "store spec is missing provider"
	errMissingAWSProvider    = "invalid provider spec. Missing AWS field in store provider spec"
	errFailedToCreateSession = "failed to create usable AWS session: %w"
	errMethodNotImplemented  = "method not implemented. secret provider: %s, method: %s"
)

// DefaultSecretStoreProvider should implement the secrets.SecretStoreProvider interface
var _ secrets.SecretStoreProvider = &DefaultSecretStoreProvider{}

// smSecretStore should implement the secrets.SecretStore interface
var _ secrets.SecretStore = &smSecretStore{}

type DefaultSecretStoreProvider struct{}

// NewSecretStore constructs a Vault based secret store with specific secret store spec.
func (p *DefaultSecretStoreProvider) NewSecretStore(spec *v1.SecretStore) (secrets.SecretStore, error) {
	providerSpec := spec.Provider
	if providerSpec == nil {
		return nil, fmt.Errorf(errMissingProviderSpec)
	}
	if providerSpec.AWS == nil {
		return nil, fmt.Errorf(errMissingAWSProvider)
	}

	config, err := auth.NewV2Config(context.TODO(), providerSpec.AWS.Region, providerSpec.AWS.Profile)
	if err != nil {
		return nil, fmt.Errorf(errFailedToCreateSession, err)
	}

	return &smSecretStore{
		client: secretsmanager.NewFromConfig(config),
	}, nil
}

type smSecretStore struct {
	client Client
}

// GetSecret retrieves ref secret value from AWS Secrets Manager.
func (s *smSecretStore) GetSecret(ctx context.Context, ref v1.ExternalSecretRef) ([]byte, error) {
	getSecretValueInput := s.buildGetSecretValueInput(ref)
	secretValueOutput, err := s.client.GetSecretValue(ctx, getSecretValueInput)
	var nf *types.ResourceNotFoundException
	if errors.As(err, &nf) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ref.Property == "" {
		if secretValueOutput.SecretString != nil {
			return []byte(*secretValueOutput.SecretString), nil
		}
		if secretValueOutput.SecretBinary != nil {
			return secretValueOutput.SecretBinary, nil
		}
		return nil, fmt.Errorf("invalid secret data. no secret string nor binary for key: %s", ref.Name)
	}
	val := s.convertSecretToGjson(secretValueOutput, ref.Property)
	if !val.Exists() {
		return nil, fmt.Errorf("key %s does not exist in secret %s", ref.Property, ref.Name)
	}
	return []byte(val.String()), nil
}

// SetSecret sets ref secret value to AWS Secrets Manager.
func (s *smSecretStore) SetSecret(ctx context.Context, ref v1.ExternalSecretRef, secretValue []byte) error {
	return fmt.Errorf(errMethodNotImplemented, "AWS Secret Manager", "SetSecret")
}

// buildGetSecretValueInput constructs target GetSecretValueInput request with specific external secret ref.
func (s *smSecretStore) buildGetSecretValueInput(ref v1.ExternalSecretRef) *secretsmanager.GetSecretValueInput {
	version := "AWSCURRENT"
	if ref.Version != "" {
		version = ref.Version
	}
	var getSecretValueInput *secretsmanager.GetSecretValueInput
	if strings.HasPrefix(version, "uuid/") {
		versionID := strings.TrimPrefix(version, "uuid/")
		getSecretValueInput = &secretsmanager.GetSecretValueInput{
			SecretId:  &ref.Name,
			VersionId: &versionID,
		}
	} else {
		getSecretValueInput = &secretsmanager.GetSecretValueInput{
			SecretId:     &ref.Name,
			VersionStage: &version,
		}
	}
	return getSecretValueInput
}

func (s *smSecretStore) convertSecretToGjson(secretValueOutput *secretsmanager.GetSecretValueOutput, refProperty string) gjson.Result {
	var payload string
	if secretValueOutput.SecretString != nil {
		payload = *secretValueOutput.SecretString
	}
	if secretValueOutput.SecretBinary != nil {
		payload = string(secretValueOutput.SecretBinary)
	}

	// We need to search if a given key with a . exists before using gjson operations.
	idx := strings.Index(refProperty, ".")
	currentRefProperty := refProperty
	if idx > -1 {
		currentRefProperty = strings.ReplaceAll(refProperty, ".", "\\.")
		val := gjson.Get(payload, currentRefProperty)
		if !val.Exists() {
			currentRefProperty = refProperty
		}
	}

	return gjson.Get(payload, currentRefProperty)
}

func init() {
	secrets.Register(&DefaultSecretStoreProvider{}, &v1.ProviderSpec{
		AWS: &v1.AWSProvider{},
	})
}
