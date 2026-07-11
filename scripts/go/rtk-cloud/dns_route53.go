package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

type route53API interface {
	ListHostedZonesByName(context.Context, *route53.ListHostedZonesByNameInput, ...func(*route53.Options)) (*route53.ListHostedZonesByNameOutput, error)
	ListResourceRecordSets(context.Context, *route53.ListResourceRecordSetsInput, ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error)
	ChangeResourceRecordSets(context.Context, *route53.ChangeResourceRecordSetsInput, ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error)
	GetChange(context.Context, *route53.GetChangeInput, ...func(*route53.Options)) (*route53.GetChangeOutput, error)
}

var loadRoute53Client = func(ctx context.Context, region string) (route53API, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return nil, fmt.Errorf("Route53 AWS credentials unavailable: %w", err)
	}
	return route53.NewFromConfig(cfg), nil
}

type route53DNSAdapter struct{ client route53API }

func (*route53DNSAdapter) Name() string { return "route53" }

func (a *route53DNSAdapter) ensureClient(ctx context.Context, adapterCtx dnsAdapterContext) error {
	if a.client != nil {
		return nil
	}
	region := firstNonEmpty(os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"), adapterCtx.Values["ROUTE53_CONTROL_PLANE_REGION"], "us-east-1")
	client, err := loadRoute53Client(ctx, region)
	if err != nil {
		return err
	}
	a.client = client
	return nil
}

func (a *route53DNSAdapter) Validate(ctx context.Context, adapterCtx dnsAdapterContext) error {
	if adapterCtx.RootDomain == "" {
		return errors.New("CLOUD_DNS_ROOT_DOMAIN is required")
	}
	return a.ensureClient(ctx, adapterCtx)
}

func (a *route53DNSAdapter) DiscoverZone(ctx context.Context, adapterCtx dnsAdapterContext) (dnsZone, error) {
	if err := a.ensureClient(ctx, adapterCtx); err != nil {
		return dnsZone{}, err
	}
	target := strings.TrimSuffix(adapterCtx.RootDomain, ".") + "."
	input := &route53.ListHostedZonesByNameInput{DNSName: aws.String(target), MaxItems: aws.Int32(100)}
	matches := []dnsZone{}
	for {
		out, err := a.client.ListHostedZonesByName(ctx, input)
		if err != nil {
			return dnsZone{}, fmt.Errorf("Route53 hosted-zone discovery: %w", err)
		}
		for _, hosted := range out.HostedZones {
			if aws.ToString(hosted.Name) != target || (hosted.Config != nil && hosted.Config.PrivateZone) {
				continue
			}
			matches = append(matches, dnsZone{Name: adapterCtx.RootDomain, ID: aws.ToString(hosted.Id)})
		}
		if !out.IsTruncated || out.NextDNSName == nil {
			break
		}
		input.DNSName = out.NextDNSName
		input.HostedZoneId = out.NextHostedZoneId
	}
	if len(matches) != 1 {
		return dnsZone{}, fmt.Errorf("Route53 requires exactly one public hosted zone for %s; found %d", adapterCtx.RootDomain, len(matches))
	}
	return matches[0], nil
}

func route53Type(recordType string) types.RRType { return types.RRType(recordType) }

func route53Value(recordType, value string) string {
	if recordType == "TXT" {
		return strconv.Quote(value)
	}
	return value
}

func unquoteRoute53Value(recordType, value string) string {
	if recordType == "TXT" {
		if decoded, err := strconv.Unquote(value); err == nil {
			return decoded
		}
	}
	return value
}

func (a *route53DNSAdapter) GetRecordSet(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, name, recordType string) (dnsRecordSet, error) {
	if err := a.ensureClient(ctx, adapterCtx); err != nil {
		return dnsRecordSet{}, err
	}
	fqdn := strings.TrimSuffix(name, ".") + "."
	out, err := a.client.ListResourceRecordSets(ctx, &route53.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(zone.ID), StartRecordName: aws.String(fqdn), StartRecordType: route53Type(recordType), MaxItems: aws.Int32(1),
	})
	if err != nil {
		return dnsRecordSet{}, err
	}
	record := dnsRecordSet{Name: strings.TrimSuffix(name, "."), Type: recordType}
	if len(out.ResourceRecordSets) == 0 {
		return record, nil
	}
	rr := out.ResourceRecordSets[0]
	if aws.ToString(rr.Name) != fqdn || string(rr.Type) != recordType {
		return record, nil
	}
	record.TTL = int(aws.ToInt64(rr.TTL))
	for _, value := range rr.ResourceRecords {
		record.Values = append(record.Values, unquoteRoute53Value(recordType, aws.ToString(value.Value)))
	}
	return record, nil
}

func route53Record(record dnsRecordSet) types.ResourceRecordSet {
	values := make([]types.ResourceRecord, 0, len(record.Values))
	for _, value := range record.Values {
		values = append(values, types.ResourceRecord{Value: aws.String(route53Value(record.Type, value))})
	}
	ttl := int64(record.TTL)
	return types.ResourceRecordSet{Name: aws.String(strings.TrimSuffix(record.Name, ".") + "."), Type: route53Type(record.Type), TTL: &ttl, ResourceRecords: values}
}

func (a *route53DNSAdapter) change(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, action types.ChangeAction, record dnsRecordSet) error {
	if err := a.ensureClient(ctx, adapterCtx); err != nil {
		return err
	}
	out, err := a.client.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zone.ID),
		ChangeBatch:  &types.ChangeBatch{Changes: []types.Change{{Action: action, ResourceRecordSet: awsRoute53RecordPtr(record)}}},
	})
	if err != nil {
		return err
	}
	if out.ChangeInfo == nil || out.ChangeInfo.Id == nil {
		return errors.New("Route53 change response missing change ID")
	}
	return a.waitChange(ctx, adapterCtx, aws.ToString(out.ChangeInfo.Id))
}

func awsRoute53RecordPtr(record dnsRecordSet) *types.ResourceRecordSet {
	rr := route53Record(record)
	return &rr
}

func (a *route53DNSAdapter) waitChange(ctx context.Context, adapterCtx dnsAdapterContext, id string) error {
	timeout, _ := strconv.Atoi(firstNonEmpty(adapterCtx.Values["DNS_PROPAGATION_TIMEOUT_SECONDS"], "900"))
	interval, _ := strconv.Atoi(firstNonEmpty(adapterCtx.Values["DNS_PROPAGATION_INTERVAL_SECONDS"], "10"))
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for time.Now().Before(deadline) {
		out, err := a.client.GetChange(ctx, &route53.GetChangeInput{Id: aws.String(id)})
		if err != nil {
			return err
		}
		if out.ChangeInfo != nil && out.ChangeInfo.Status == types.ChangeStatusInsync {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
	}
	return fmt.Errorf("Route53 change did not reach INSYNC before timeout")
}

func (a *route53DNSAdapter) UpsertRecordSet(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, record dnsRecordSet) error {
	return a.change(ctx, adapterCtx, zone, types.ChangeActionUpsert, record)
}

func (a *route53DNSAdapter) DeleteRecordValues(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, record dnsRecordSet) error {
	existing, err := a.GetRecordSet(ctx, adapterCtx, zone, record.Name, record.Type)
	if err != nil || len(existing.Values) == 0 {
		return err
	}
	remaining := removeDNSValues(existing.Values, record.Values)
	if len(remaining) == len(existing.Values) {
		return nil
	}
	if len(remaining) == 0 {
		return a.change(ctx, adapterCtx, zone, types.ChangeActionDelete, existing)
	}
	existing.Values = remaining
	return a.change(ctx, adapterCtx, zone, types.ChangeActionUpsert, existing)
}

func (a *route53DNSAdapter) PresentDNS01Challenge(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, domain, value string) error {
	name, err := dns01RecordName(zone.Name, domain)
	if err != nil {
		return err
	}
	existing, err := a.GetRecordSet(ctx, adapterCtx, zone, name, "TXT")
	if err != nil {
		return err
	}
	ttl, _ := strconv.Atoi(adapterCtx.Values["DNS_RECORD_TTL"])
	return a.UpsertRecordSet(ctx, adapterCtx, zone, dnsRecordSet{Name: name, Type: "TXT", Values: mergeDNSValues(existing.Values, value), TTL: ttl, Purpose: "acme-dns01"})
}

func (a *route53DNSAdapter) CleanupDNS01Challenge(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, domain, value string) error {
	name, err := dns01RecordName(zone.Name, domain)
	if err != nil {
		return err
	}
	ttl, _ := strconv.Atoi(adapterCtx.Values["DNS_RECORD_TTL"])
	return a.DeleteRecordValues(ctx, adapterCtx, zone, dnsRecordSet{Name: name, Type: "TXT", Values: []string{value}, TTL: ttl})
}

func (a *route53DNSAdapter) CollectEvidence(_ context.Context, ctx dnsAdapterContext, zone dnsZone) error {
	return writeDNSProviderState(ctx, a.Name(), map[string]any{"adapter": a.Name(), "zone_name": zone.Name, "hosted_zone_id": zone.ID})
}
