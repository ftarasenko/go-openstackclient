package network

import (
	"context"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/agents"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgp/peers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgp/speakers"
	"github.com/gophercloud/gophercloud/v2/pagination"
)

// BGP dynamic routing (neutron-dynamic-routing's bgp extension; the
// dynamic-routing-agent calls need bgp_dragent_scheduler too). The create
// and update functions take gophercloud's builders; its speakers.CreateOpts
// and UpdateOpts always send both advertise_* booleans, so a caller that
// wants neutron's defaults, or to leave them as they are, passes a builder
// of its own.

// byName lists collection?name=ref with page and keeps the exact matches,
// for the collections whose gophercloud List takes no filter. The exact match
// guards a server that ignores the query.
func byName[T any](ctx context.Context, c *gophercloud.ServiceClient, collection, ref string,
	page func(pagination.PageResult) pagination.Page, extract func(pagination.Page) ([]T, error), name func(T) string,
) ([]T, error) {
	u := c.ServiceURL(collection) + "?" + url.Values{"name": {ref}}.Encode()
	pages, err := pagination.NewPager(c, u, page).AllPages(ctx)
	all, err := extracted(pages, err, extract)
	if err != nil {
		return nil, err
	}
	kept := all[:0]
	for _, v := range all {
		if name(v) == ref {
			kept = append(kept, v)
		}
	}
	return kept, nil
}

// FindBGPSpeakerID resolves a BGP speaker name or ID to an ID.
func FindBGPSpeakerID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "BGP speaker", ref, func(ctx context.Context) ([]speakers.BGPSpeaker, error) {
		return byName(ctx, c, "bgp-speakers", ref, func(r pagination.PageResult) pagination.Page {
			return speakers.BGPSpeakerPage{SinglePageBase: pagination.SinglePageBase(r)}
		}, speakers.ExtractBGPSpeakers, func(s speakers.BGPSpeaker) string { return s.Name })
	}, func(s speakers.BGPSpeaker) string { return s.ID })
}

// FindBGPPeerID resolves a BGP peer name or ID to an ID.
func FindBGPPeerID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "BGP peer", ref, func(ctx context.Context) ([]peers.BGPPeer, error) {
		return byName(ctx, c, "bgp-peers", ref, func(r pagination.PageResult) pagination.Page {
			return peers.BGPPeerPage{SinglePageBase: pagination.SinglePageBase(r)}
		}, peers.ExtractBGPPeers, func(p peers.BGPPeer) string { return p.Name })
	}, func(p peers.BGPPeer) string { return p.ID })
}

// ListBGPSpeakers lists every BGP speaker.
func ListBGPSpeakers(ctx context.Context, c *gophercloud.ServiceClient) ([]speakers.BGPSpeaker, error) {
	pages, err := speakers.List(c).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing BGP speakers")
	}
	all, err := speakers.ExtractBGPSpeakers(pages)
	return all, wrap(err, "parsing BGP speaker list")
}

// ListDRAgentBGPSpeakers lists the BGP speakers a dynamic routing agent
// hosts (GET /agents/{id}/bgp-drinstances).
func ListDRAgentBGPSpeakers(ctx context.Context, c *gophercloud.ServiceClient, agentID string) ([]speakers.BGPSpeaker, error) {
	pages, err := agents.ListBGPSpeakers(c, agentID).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing BGP speakers hosted by agent %s", agentID)
	}
	all, err := agents.ExtractBGPSpeakers(pages)
	return all, wrap(err, "parsing BGP speaker list")
}

// GetBGPSpeaker reads one BGP speaker.
func GetBGPSpeaker(ctx context.Context, c *gophercloud.ServiceClient, id string) (*speakers.BGPSpeaker, error) {
	s, err := speakers.Get(ctx, c, id).Extract()
	return s, wrap(err, "getting BGP speaker %s", id)
}

// CreateBGPSpeaker creates a BGP speaker.
func CreateBGPSpeaker(ctx context.Context, c *gophercloud.ServiceClient, opts speakers.CreateOptsBuilder) (*speakers.BGPSpeaker, error) {
	s, err := speakers.Create(ctx, c, opts).Extract()
	return s, wrap(err, "creating BGP speaker")
}

// UpdateBGPSpeaker updates a BGP speaker.
func UpdateBGPSpeaker(ctx context.Context, c *gophercloud.ServiceClient, id string, opts speakers.UpdateOptsBuilder) (*speakers.BGPSpeaker, error) {
	s, err := speakers.Update(ctx, c, id, opts).Extract()
	return s, wrap(err, "updating BGP speaker %s", id)
}

// DeleteBGPSpeaker deletes a BGP speaker.
func DeleteBGPSpeaker(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(speakers.Delete(ctx, c, id).ExtractErr(), "deleting BGP speaker %s", id)
}

// AddBGPSpeakerGatewayNetwork adds a gateway network to a BGP speaker.
func AddBGPSpeakerGatewayNetwork(ctx context.Context, c *gophercloud.ServiceClient, speakerID, networkID string) error {
	_, err := speakers.AddGatewayNetwork(ctx, c, speakerID, speakers.AddGatewayNetworkOpts{NetworkID: networkID}).Extract()
	return wrap(err, "adding network %s to BGP speaker %s", networkID, speakerID)
}

// RemoveBGPSpeakerGatewayNetwork removes a gateway network from a BGP
// speaker.
func RemoveBGPSpeakerGatewayNetwork(ctx context.Context, c *gophercloud.ServiceClient, speakerID, networkID string) error {
	err := speakers.RemoveGatewayNetwork(ctx, c, speakerID, speakers.RemoveGatewayNetworkOpts{NetworkID: networkID}).ExtractErr()
	return wrap(err, "removing network %s from BGP speaker %s", networkID, speakerID)
}

// AddBGPSpeakerPeer adds a BGP peer to a speaker.
func AddBGPSpeakerPeer(ctx context.Context, c *gophercloud.ServiceClient, speakerID, peerID string) error {
	_, err := speakers.AddBGPPeer(ctx, c, speakerID, speakers.AddBGPPeerOpts{BGPPeerID: peerID}).Extract()
	return wrap(err, "adding BGP peer %s to BGP speaker %s", peerID, speakerID)
}

// RemoveBGPSpeakerPeer removes a BGP peer from a speaker.
func RemoveBGPSpeakerPeer(ctx context.Context, c *gophercloud.ServiceClient, speakerID, peerID string) error {
	err := speakers.RemoveBGPPeer(ctx, c, speakerID, speakers.RemoveBGPPeerOpts{BGPPeerID: peerID}).ExtractErr()
	return wrap(err, "removing BGP peer %s from BGP speaker %s", peerID, speakerID)
}

// ListBGPSpeakerAdvertisedRoutes lists the routes a BGP speaker advertises.
func ListBGPSpeakerAdvertisedRoutes(ctx context.Context, c *gophercloud.ServiceClient, speakerID string) ([]speakers.AdvertisedRoute, error) {
	pages, err := speakers.GetAdvertisedRoutes(c, speakerID).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing routes advertised by BGP speaker %s", speakerID)
	}
	all, err := speakers.ExtractAdvertisedRoutes(pages)
	return all, wrap(err, "parsing advertised routes")
}

// ListBGPPeers lists every BGP peer.
func ListBGPPeers(ctx context.Context, c *gophercloud.ServiceClient) ([]peers.BGPPeer, error) {
	pages, err := peers.List(c).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing BGP peers")
	}
	all, err := peers.ExtractBGPPeers(pages)
	return all, wrap(err, "parsing BGP peer list")
}

// GetBGPPeer reads one BGP peer.
func GetBGPPeer(ctx context.Context, c *gophercloud.ServiceClient, id string) (*peers.BGPPeer, error) {
	p, err := peers.Get(ctx, c, id).Extract()
	return p, wrap(err, "getting BGP peer %s", id)
}

// CreateBGPPeer creates a BGP peer.
func CreateBGPPeer(ctx context.Context, c *gophercloud.ServiceClient, opts peers.CreateOptsBuilder) (*peers.BGPPeer, error) {
	p, err := peers.Create(ctx, c, opts).Extract()
	return p, wrap(err, "creating BGP peer")
}

// UpdateBGPPeer updates a BGP peer.
func UpdateBGPPeer(ctx context.Context, c *gophercloud.ServiceClient, id string, opts peers.UpdateOptsBuilder) (*peers.BGPPeer, error) {
	p, err := peers.Update(ctx, c, id, opts).Extract()
	return p, wrap(err, "updating BGP peer %s", id)
}

// DeleteBGPPeer deletes a BGP peer.
func DeleteBGPPeer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(peers.Delete(ctx, c, id).ExtractErr(), "deleting BGP peer %s", id)
}

// AddDRAgentBGPSpeaker schedules a BGP speaker on a dynamic routing agent.
func AddDRAgentBGPSpeaker(ctx context.Context, c *gophercloud.ServiceClient, agentID, speakerID string) error {
	err := agents.ScheduleBGPSpeaker(ctx, c, agentID, agents.ScheduleBGPSpeakerOpts{SpeakerID: speakerID}).ExtractErr()
	return wrap(err, "adding BGP speaker %s to dynamic routing agent %s", speakerID, agentID)
}

// RemoveDRAgentBGPSpeaker removes a BGP speaker from a dynamic routing
// agent.
func RemoveDRAgentBGPSpeaker(ctx context.Context, c *gophercloud.ServiceClient, agentID, speakerID string) error {
	err := agents.RemoveBGPSpeaker(ctx, c, agentID, speakerID).ExtractErr()
	return wrap(err, "removing BGP speaker %s from dynamic routing agent %s", speakerID, agentID)
}

// ListBGPSpeakerDRAgents lists the dynamic routing agents hosting a BGP
// speaker (GET /bgp-speakers/{id}/bgp-dragents).
func ListBGPSpeakerDRAgents(ctx context.Context, c *gophercloud.ServiceClient, speakerID string) ([]agents.Agent, error) {
	pages, err := agents.ListDRAgentHostingBGPSpeakers(c, speakerID).AllPages(ctx)
	if err != nil {
		return nil, wrap(err, "listing dynamic routing agents hosting BGP speaker %s", speakerID)
	}
	all, err := agents.ExtractAgents(pages)
	return all, wrap(err, "parsing dynamic routing agent list")
}
