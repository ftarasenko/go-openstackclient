package identity

import (
	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"
)

// identityClient returns a service client wired to the mock server with the
// keystone service type, mirroring how auth.Client.Identity does. The identity
// client carries no microversion header, so Microversion is left empty.
func identityClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "identity"
	return sc
}
