package aws

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/openbao/openbao/sdk/v2/logical"
	"github.com/stretchr/testify/require"
)

// stubSTS returns canned STS responses. Calling a method without a canned
// response panics because the embedded interface is nil.
type stubSTS struct {
	STSAPI
	assumeRole         *sts.AssumeRoleOutput
	getFederationToken *sts.GetFederationTokenOutput
}

func (s stubSTS) AssumeRole(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	return s.assumeRole, nil
}

func (s stubSTS) GetFederationToken(context.Context, *sts.GetFederationTokenInput, ...func(*sts.Options)) (*sts.GetFederationTokenOutput, error) {
	return s.getFederationToken, nil
}

// STS-compatible services such as MinIO or RustFS return only Credentials and
// omit AssumedRoleUser. That must not crash the plugin.
func TestBackend_assumeRoleOptionalAssumedRoleUser(t *testing.T) {
	const roleArn = "arn:aws:iam::000000000000:role/test"
	const assumedArn = "arn:aws:sts::000000000000:assumed-role/test/session"

	cases := map[string]struct {
		assumedRoleUser *ststypes.AssumedRoleUser
		wantArn         string
	}{
		"without AssumedRoleUser": {
			assumedRoleUser: nil,
			wantArn:         "",
		},
		"with AssumedRoleUser": {
			assumedRoleUser: &ststypes.AssumedRoleUser{
				Arn:           aws.String(assumedArn),
				AssumedRoleId: aws.String("AROAEXAMPLE:session"),
			},
			wantArn: assumedArn,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			config := logical.TestBackendConfig()
			config.StorageView = &logical.InmemStorage{}

			b := Backend(config)
			require.NoError(t, b.Setup(ctx, config))

			resp, err := b.HandleRequest(ctx, &logical.Request{
				Operation: logical.UpdateOperation,
				Path:      "roles/test",
				Storage:   config.StorageView,
				Data: map[string]interface{}{
					"credential_type": "assumed_role",
					"role_arns":       roleArn,
				},
			})
			require.NoError(t, err)
			require.False(t, resp != nil && resp.IsError(), "failed to write role: %#v", resp)

			expiration := time.Now().Add(time.Hour)
			b.stsClient = stubSTS{assumeRole: &sts.AssumeRoleOutput{
				Credentials: &ststypes.Credentials{
					AccessKeyId:     aws.String("ASIAEXAMPLE"),
					SecretAccessKey: aws.String("secret"),
					SessionToken:    aws.String("token"),
					Expiration:      &expiration,
				},
				AssumedRoleUser: tc.assumedRoleUser,
			}}

			resp, err = b.HandleRequest(ctx, &logical.Request{
				Operation: logical.ReadOperation,
				Path:      "sts/test",
				Storage:   config.StorageView,
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.False(t, resp.IsError(), "failed to read credentials: %#v", resp)

			require.Equal(t, "ASIAEXAMPLE", resp.Data["access_key"])
			require.Equal(t, "secret", resp.Data["secret_key"])
			require.Equal(t, "token", resp.Data["session_token"])
			require.Equal(t, "token", resp.Data["security_token"])
			if tc.wantArn == "" {
				require.NotContains(t, resp.Data, "arn")
			} else {
				require.Equal(t, tc.wantArn, resp.Data["arn"])
			}
		})
	}
}

// A response without complete credentials must result in an error, not a panic.
func TestBackend_stsIncompleteCredentials(t *testing.T) {
	expiration := time.Now().Add(time.Hour)
	incomplete := map[string]*ststypes.Credentials{
		"nil credentials": nil,
		"missing session token": {
			AccessKeyId:     aws.String("ASIAEXAMPLE"),
			SecretAccessKey: aws.String("secret"),
			Expiration:      &expiration,
		},
		"missing expiration": {
			AccessKeyId:     aws.String("ASIAEXAMPLE"),
			SecretAccessKey: aws.String("secret"),
			SessionToken:    aws.String("token"),
		},
	}

	roles := map[string]map[string]interface{}{
		"assumed_role": {
			"credential_type": "assumed_role",
			"role_arns":       "arn:aws:iam::000000000000:role/test",
		},
		"federation_token": {
			"credential_type": "federation_token",
			"policy_document": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
		},
	}

	for roleName, roleData := range roles {
		for name, creds := range incomplete {
			t.Run(roleName+"/"+name, func(t *testing.T) {
				ctx := context.Background()
				config := logical.TestBackendConfig()
				config.StorageView = &logical.InmemStorage{}

				b := Backend(config)
				require.NoError(t, b.Setup(ctx, config))

				resp, err := b.HandleRequest(ctx, &logical.Request{
					Operation: logical.UpdateOperation,
					Path:      "roles/test",
					Storage:   config.StorageView,
					Data:      roleData,
				})
				require.NoError(t, err)
				require.False(t, resp != nil && resp.IsError(), "failed to write role: %#v", resp)

				b.stsClient = stubSTS{
					assumeRole:         &sts.AssumeRoleOutput{Credentials: creds},
					getFederationToken: &sts.GetFederationTokenOutput{Credentials: creds},
				}

				_, err = b.HandleRequest(ctx, &logical.Request{
					Operation: logical.ReadOperation,
					Path:      "sts/test",
					Storage:   config.StorageView,
				})
				require.ErrorContains(t, err, "complete credentials")
			})
		}
	}
}
