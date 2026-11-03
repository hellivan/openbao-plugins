# aws secrets: nil pointer panic when the STS response has no AssumedRoleUser

Repository: openbao/openbao-plugins
Title: `aws secrets: nil pointer panic when STS response has no AssumedRoleUser`

## Description

Reading `sts/<role>` for a role with `credential_type=assumed_role` against an STS-compatible service that omits the `AssumedRoleUser` element in its `AssumeRole` response crashes the plugin process with a nil pointer dereference.

AWS STS always returns `AssumedRoleUser`, but other implementations do not. RustFS 1.0.x only returns `Credentials`. The AWS SDK for Go v2 models every response field as a nullable pointer, so none of them should be dereferenced without a check.

## Environment

- OpenBao 2.7.2
- `openbao-plugin-secrets-aws` v0.3.2
- STS endpoint: RustFS 1.0.4 (`sts_endpoint` pointing to RustFS)

## Steps to reproduce

1. Mount the plugin and configure it with an `sts_endpoint` of a service that omits `AssumedRoleUser` (RustFS):
   ```
   bao secrets enable aws
   bao write aws/config/root access_key=<key> secret_key=<secret> sts_endpoint=https://rustfs.example.com region=eu-central-1
   bao write aws/roles/test credential_type=assumed_role role_arns=arn:aws:iam::000000000000:role/test default_sts_ttl=1h max_sts_ttl=1h
   ```
2. Read credentials:
   ```
   bao read aws/creds/test
   ```

## Actual behavior

The request fails and the plugin process exits:

```
Error making API request.

URL: GET https://openbao.example.com/v1/aws/creds/test
Code: 500. Errors:

* 1 error occurred:
	* rpc error: code = Unavailable desc = error reading from server: EOF
```

Server log:

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0xfd7f03]

goroutine 226 [running]:
github.com/openbao/openbao-plugins/secrets/aws.(*backend).assumeRole(...)
        secrets/aws/secret_access_keys.go:284 +0xea3
github.com/openbao/openbao-plugins/secrets/aws.(*backend).pathCredsRead(...)
        secrets/aws/path_user.go:156 +0xd25
```

Line 284 is `"arn": *tokenResp.AssumedRoleUser.Arn,`.

## Expected behavior

The plugin should not crash. It should return the credentials, leaving out `arn` if the service did not return an `AssumedRoleUser`. If the response has no complete credentials, it should return an error instead of panicking.

## Analysis

- `*tokenResp.AssumedRoleUser.Arn` is dereferenced unchecked in `assumeRole`.
- `tokenResp.Credentials` and its fields (`AccessKeyId`, `SecretAccessKey`, `SessionToken`, `Expiration`) are dereferenced unchecked as well, in both `assumeRole` and `getFederationToken`.
- For reference, `AssumedRoleUser.Arn` and `AssumedRoleId` are marked as required in the [AWS API reference](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumedRoleUser.html), but the `AssumedRoleUser` element itself is not marked as required in the [AssumeRole response elements](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html).

The RustFS side is a separate issue: its AssumeRole handler builds the response with only `credentials` set (`rustfs/src/admin/handlers/sts.rs`). I will link that issue here once it exists.

## Proposed fix

A pull request follows: validate the STS credentials once and return an error when they are incomplete, and only add `arn` to the response if `AssumedRoleUser` is present.

## Related

- #11 (MinIO STS credentials, feature request)
