package keymanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/secrets"
	"github.com/gophercloud/gophercloud/v2/pagination"
)

// firstPage fetches one page of a listing, or nil when it is empty.
func firstPage(ctx context.Context, pager pagination.Pager) (pagination.Page, error) {
	var first pagination.Page
	err := pager.EachPage(ctx, func(_ context.Context, p pagination.Page) (bool, error) {
		first = p
		return false, nil
	})
	return first, err
}

// ListSecrets lists one window of secrets: opts.Limit entries from
// opts.Offset.
func ListSecrets(ctx context.Context, c *gophercloud.ServiceClient, opts secrets.ListOpts) ([]secrets.Secret, error) {
	page, err := firstPage(ctx, secrets.List(c, opts))
	if err != nil || page == nil {
		return nil, wrap(err, "listing secrets")
	}
	all, err := secrets.ExtractSecrets(page)
	return all, wrap(err, "parsing secret list")
}

// GetSecret returns a secret's metadata.
func GetSecret(ctx context.Context, c *gophercloud.ServiceClient, id string) (*secrets.Secret, error) {
	s, err := secrets.Get(ctx, c, id).Extract()
	return s, wrap(err, "getting secret %s", id)
}

// GetSecretPayload returns a secret's decrypted payload in contentType.
func GetSecretPayload(ctx context.Context, c *gophercloud.ServiceClient, id, contentType string) ([]byte, error) {
	p, err := secrets.GetPayload(ctx, c, id, secrets.GetPayloadOpts{PayloadContentType: contentType}).Extract()
	return p, wrap(err, "getting payload of secret %s", id)
}

// CreateSecret stores a secret and returns its UUID.
func CreateSecret(ctx context.Context, c *gophercloud.ServiceClient, opts secrets.CreateOptsBuilder) (string, error) {
	created, err := secrets.Create(ctx, c, opts).Extract()
	if err != nil {
		return "", wrap(err, "storing secret")
	}
	id, err := RefUUID(created.SecretRef, "Secret")
	if err != nil {
		return "", fmt.Errorf("storing secret: barbican answered secret_ref %q: %w", created.SecretRef, err)
	}
	return id, nil
}

// SetSecretPayload adds a text/plain payload to a secret stored without one.
func SetSecretPayload(ctx context.Context, c *gophercloud.ServiceClient, id, payload string) error {
	opts := secrets.UpdateOpts{ContentType: "text/plain", Payload: payload}
	return wrap(secrets.Update(ctx, c, id, opts).ExtractErr(), "updating secret %s", id)
}

// ErrSecretHasConsumers is DeleteSecret's refusal of a secret with consumers.
var ErrSecretHasConsumers = errors.New("secret cannot be deleted as it has consumers")

// DeleteSecret deletes a secret. Unless force is set it first reads the
// secret's consumers and refuses with ErrSecretHasConsumers, as
// barbicanclient does below microversion 1.2: at 1.1 barbican itself deletes
// a secret with consumers without asking.
func DeleteSecret(ctx context.Context, c *gophercloud.ServiceClient, id string, force bool) error {
	var s struct {
		Consumers []json.RawMessage `json:"consumers"`
	}
	if err := secrets.Get(ctx, c, id).ExtractInto(&s); err != nil {
		return wrap(err, "getting secret %s", id)
	}
	if len(s.Consumers) > 0 && !force {
		return ErrSecretHasConsumers
	}
	return wrap(secrets.Delete(ctx, c, id).ExtractErr(), "deleting secret %s", id)
}

// Secret consumers are a raw fallback: gophercloud v2.15.0's keymanager has
// consumers for containers only. They are barbican's microversion 1.1 API,
// which every release from Zed carries.
//
//	POST|DELETE /v1/secrets/{id}/consumers   {"service", "resource_type", "resource_id"}
//	GET         /v1/secrets/{id}/consumers?limit=&offset=

// SecretConsumer is a service resource registered as using a secret.
type SecretConsumer struct {
	Service      string `json:"service"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	// Created is set on a listed consumer and ignored on a write.
	Created string `json:"created"`
}

func (s SecretConsumer) body() map[string]string {
	return map[string]string{"service": s.Service, "resource_type": s.ResourceType, "resource_id": s.ResourceID}
}

// AddSecretConsumer registers a consumer of a secret.
func AddSecretConsumer(ctx context.Context, c *gophercloud.ServiceClient, id string, consumer SecretConsumer) error {
	resp, err := c.Post(ctx, c.ServiceURL("secrets", id, "consumers"), consumer.body(), nil,
		&gophercloud.RequestOpts{OkCodes: []int{http.StatusOK, http.StatusCreated}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return wrap(err, "registering consumer of secret %s", id)
}

// RemoveSecretConsumer removes a consumer from a secret.
func RemoveSecretConsumer(ctx context.Context, c *gophercloud.ServiceClient, id string, consumer SecretConsumer) error {
	resp, err := c.Delete(ctx, c.ServiceURL("secrets", id, "consumers"),
		&gophercloud.RequestOpts{JSONBody: consumer.body(), OkCodes: []int{http.StatusOK, http.StatusNoContent}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return wrap(err, "removing consumer from secret %s", id)
}

// ListSecretConsumers lists one window of a secret's consumers.
func ListSecretConsumers(ctx context.Context, c *gophercloud.ServiceClient, id string, limit, offset int) ([]SecretConsumer, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}
	var body struct {
		Consumers []SecretConsumer `json:"consumers"`
	}
	resp, err := c.Get(ctx, c.ServiceURL("secrets", id, "consumers")+"?"+q.Encode(), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return nil, wrap(err, "listing consumers of secret %s", id)
	}
	return body.Consumers, nil
}
