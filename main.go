package main

import (
	"context"
	"errors"
	"log"

	"github.com/cloudflare/cloudflare-go"
	csbouncer "github.com/crowdsecurity/go-cs-bouncer"
)

func clearExistingCrowdSecIpList(ctx context.Context, api *cloudflare.API) {
	ipLists, err := api.ListIPLists(ctx)
	if err != nil {
		log.Fatal(err)
	}

	id, err := getCrowdSecIPListId(ipLists)
	if err != nil {
		return
	}

 	_, err = api.DeleteIPList(ctx, id)
	if err != nil {
		log.Fatal(err)
	}

}

func getCrowdSecIPListId(ipLists []cloudflare.IPList) (string, error) {
	for _, ipList := range ipLists {
		if ipList.Name == "crowdsec" {
			return ipList.ID, nil
		}
	}
	return "", errors.New("crowdsec ip list not found")
}

func main() {

	ctx := context.Background()
	conf, err := NewConfig("./conf.yaml")
	if err != nil {
		log.Fatal(err)
	}
	cfApi, _ := cloudflare.NewWithAPIToken(conf.CloudflareAPIToken, cloudflare.UsingAccount(conf.CloudflareAccountID))

	clearExistingCrowdSecIpList(ctx, cfApi)
	ipList, err := cfApi.CreateIPList(ctx, "crowdsec", "IP list managed by crowdsec bouncer", "ip")
	if err != nil {
		log.Fatal(err)
	}

	ipFilters := make([]cloudflare.Filter, 1)
	ipFilters[0] = cloudflare.Filter{Expression: "ip.src in $crowdsec"}

	firewallRules := make([]cloudflare.FirewallRule, 1)
	firewallRules[0] = cloudflare.FirewallRule{Filter: ipFilters[0], Action: "challenge"}
	_, err = cfApi.CreateFirewallRules(ctx, conf.CloudflareZoneID, firewallRules)

	if err != nil {
		log.Fatal(err)
	}

	csLapi := &csbouncer.StreamBouncer{
		APIKey:         conf.CrowdSecLAPIKey,
		APIUrl:         conf.CrowdSecLAPIUrl,
		TickerInterval: conf.UpdateFrequencyYAML,
	}

	cloudflareIdByIp := make(map[string]string)

	if err := csLapi.Init(); err != nil {
		log.Fatalf(err.Error())
	}

	go csLapi.Run()

	for streamDecision := range csLapi.Stream {
		deleteIpsMap := make(map[cloudflare.IPListItemDeleteItemRequest]bool)
		addIpsMap := make(map[cloudflare.IPListItemCreateRequest]bool)

		for _, decision := range streamDecision.Deleted {
			if cloudflareIdByIp[*decision.Value] != "" {
				deleteIpsMap[cloudflare.IPListItemDeleteItemRequest{ID: cloudflareIdByIp[*decision.Value]}] = true
				delete(cloudflareIdByIp, *decision.Value)
			}
		}

		for _, decision := range streamDecision.New {
			addIpsMap[cloudflare.IPListItemCreateRequest{
				IP:      *decision.Value,
				Comment: "Added by crowdsec bouncer",
			}] = true
		}

		addIps := make([]cloudflare.IPListItemCreateRequest, 0)
		for k, _ := range addIpsMap {
			addIps = append(addIps, k)
		}

		if len(addIps) > 0 {
			ipItems, err := cfApi.CreateIPListItems(ctx, ipList.ID, addIps)
			if err != nil {
				log.Fatal(err)
			}

			for _, ipItem := range ipItems {
				cloudflareIdByIp[ipItem.IP] = ipItem.ID
			}
		}

		deleteIps := make([]cloudflare.IPListItemDeleteItemRequest, 0)
		for k, _ := range deleteIpsMap {
			deleteIps = append(deleteIps, k)
		}

		if len(deleteIps) > 0 {
			cfApi.DeleteIPListItems(ctx, ipList.ID, cloudflare.IPListItemDeleteRequest{Items: deleteIps})
		}

	}

}
