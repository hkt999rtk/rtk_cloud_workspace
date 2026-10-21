package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Extend the same native registry with genuine signed CRLs for its nine actual
// authorities. Original publications come from their immutable producer files;
// intermediate/Device publications use only the original in-memory issuer keys.
// A later actual import can update this protocol model, never the old receipt.
func installHistoricalLateCRLProtocol(t *testing.T, f *deviceTrustExecutionContract, roots, intermediates map[string]environmentCreateRegisteredIssuer) {
	t.Helper()
	if f.protocol["historical_crls"] != nil {
		t.Fatal("historical CRL protocol already installed")
	}
	records := map[string]managedLateCRLRecord{}
	for domain, root := range roots {
		if domain == "device" {
			records[root.ID] = historicalNativeSignedCRL(t, root, f.authorityKeys[root.ID])
			continue
		}
		entry := createOpenBaoACKCompletionEntry(f.e.Journal, "pki-initial-root-crl-"+domain)
		var receipt map[string]string
		if entry == nil || entry.State != "completed" || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt["issuer_id"] != root.ID {
			t.Fatal("original CRL producer receipt unavailable", domain)
		}
		path, err := f.store.safePath(receipt["crl_file"])
		if err != nil {
			t.Fatal(err)
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil || environmentPreflightBytesSHA256(raw) != receipt["crl_pem_sha256"] {
			t.Fatal("original CRL body differs from its receipt", domain, err)
		}
		record, err := managedLateOriginalRecord(root.ID, raw)
		if err != nil {
			t.Fatal("original signed CRL record", domain, err)
		}
		records[root.ID] = record
	}
	for domain, issuer := range intermediates {
		key := f.authorityKeys[issuer.ID]
		if domain == "service" {
			key = f.issuerKey
		}
		number := int64(1)
		if domain == "openbao_tls" {
			number = 3
		}
		records[issuer.ID] = historicalNativeSignedCRL(t, issuer, key, number)
	}
	if len(records) != 9 {
		t.Fatal("native registry does not contain exactly nine original authorities", len(records))
	}
	f.protocol["historical_crls"] = records
	history := make(map[string][]managedLateCRLRecord, len(records))
	for id, record := range records {
		history[id] = []managedLateCRLRecord{record}
	}
	f.protocol["historical_crl_history"] = history
	f.protocol["historical_crl_acks"] = []managedLatePriorACK{}
	f.protocol["historical_crl_caches"] = map[string]map[string]any{}
	f.publish(t)
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,re,sys,stat
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
a=sys.argv[1:];query=a[-1] if a else '';crls=d['historical_crls'];history=d.get('historical_crl_history',{})
if 'get' in a:
 i=a.index('get');kind=a[i+1];ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
 if kind=='pods' and ('-l' in a or '--selector' in a):
  marker='-l' if '-l' in a else '--selector';key,value=a[a.index(marker)+1].split('=',1)
  print(json.dumps({'apiVersion':'v1','kind':'PodList','items':[v for v in d['objects'].values() if v['kind']=='Pod' and v.get('metadata',{}).get('namespace')==ns and v.get('metadata',{}).get('labels',{}).get(key)==value]}));sys.exit()
 if kind=='namespace':
  print(json.dumps({'apiVersion':'v1','kind':'NamespaceList','items':[v for v in d['objects'].values() if v['kind']=='Namespace']}));sys.exit()
 requested=set(kind.lower().split(','));cluster={'clusterrole','clusterrolebinding','ingressclass','validatingwebhookconfiguration'}
 listing=',' in kind or i+2>=len(a) or a[i+2].startswith('-')
 if listing and (',' in kind or requested.issubset(cluster)):
  selectors={}
  if '-l' in a or '--selector' in a:
   marker='-l' if '-l' in a else '--selector'
   for selector in a[a.index(marker)+1].split(','):
    if selector.count('=')!=1:sys.exit('unsupported exact native selector')
    key,value=selector.split('=',1);selectors[key]=value
  rows=[v for v in d['objects'].values() if v['kind'].lower() in requested and (v.get('metadata',{}).get('namespace')==ns if ns else not v.get('metadata',{}).get('namespace')) and all(v.get('metadata',{}).get('labels',{}).get(k)==value for k,value in selectors.items())]
  print(json.dumps({'apiVersion':'v1','kind':'List','metadata':{},'items':rows}));sys.exit()
 name=a[i+2];v=d['objects'].get(kind+'/'+ns+'/'+name)
 if v is not None:print(json.dumps(v))
 sys.exit()
elif 'registry-read' in a and 'FROM (SELECT DISTINCT ON(issuer_id)' in query and 'FROM pki_crls WHERE issuer_id IN (' in query:
 ids=query.split('WHERE issuer_id IN (',1)[1].split(')',1)[0].replace("'",'').split(',')
 if len(ids)!=9 or len(set(ids))!=9 or any(i not in crls for i in ids):sys.exit('unknown selected CRL authority')
 print(json.dumps([crls[i] for i in sorted(ids)]))
elif 'registry-read' in a and query.startswith("SELECT json_build_object('prior'"):
 ids=set(re.findall("issuer_id='([^']+)'",query))
 if len(ids)!=1 or next(iter(ids)) not in crls:sys.exit('unknown native revocation snapshot authority')
 print(json.dumps({'prior':crls[next(iter(ids))],'bindings':[],'certificates':[]}))
elif 'registry-read' in a and "FROM pki_crls WHERE issuer_id='" in query and 'AND number>=3' in query:
 issuer=query.split("WHERE issuer_id='",1)[1].split("'",1)[0]
 rows=history.get(issuer,[])
 if not rows or any(row['issuer_id']!=issuer for row in rows):sys.exit('unknown native CRL history authority')
 print(json.dumps(sorted([row for row in rows if int(row['crl_number'])>=3],key=lambda row:int(row['crl_number']))))
elif 'registry-read' in a and query.startswith("SELECT json_build_object('imports'"):
 ids=set(re.findall("issuer_id='([^']+)'",query))
 if len(ids)!=1:sys.exit('native publication selects multiple authorities')
 issuer=next(iter(ids));publication=d.get('historical_crl_publications',{}).get(issuer)
 if publication is None or any(row['digest']!=crls[issuer]['crl_sha256'] for row in publication['acks']):sys.exit('actual native publication unavailable')
 print(json.dumps(publication))
elif 'registry-read' in a and 'FROM pki_crl_acknowledgments WHERE issuer_id=' in query:
 issuer=query.split("WHERE issuer_id='",1)[1].split("'",1)[0];digest=query.split("AND digest='",1)[1].split("'",1)[0]
 print(json.dumps(sorted([x for x in d['historical_crl_acks'] if x['issuer_id']==issuer and x['digest']==digest],key=lambda x:x['consumer_id'])))
elif ('mqtt-root-crl-native-state' in a or 'openbao-tls-root-crl-native-state' in a) and 'exec' in a:
 ns=a[a.index('--namespace')+1];podName=a[a.index('exec')+1].removeprefix('pod/');container=a[a.index('--container')+1];path=a[-1]
 pod=d['objects'].get('Pod/'+ns+'/'+podName)
 if pod is None:sys.exit('native cache Pod missing')
 containers=[c for c in pod['spec']['containers'] if c['name']==container]
 if len(containers)!=1:sys.exit('native cache container missing')
 mounts=[m for m in containers[0].get('volumeMounts',[]) if path.startswith(m['mountPath'].rstrip('/')+'/') and not m.get('readOnly') and 'subPath' not in m]
 if len(mounts)!=1:sys.exit('cache path is not owned by exactly one durable mount')
 volumes=[v for v in pod['spec'].get('volumes',[]) if v['name']==mounts[0]['name'] and 'persistentVolumeClaim' in v]
 if len(volumes)!=1:sys.exit('native cache has no durable volume')
 claim=d['objects'].get('PersistentVolumeClaim/'+ns+'/'+volumes[0]['persistentVolumeClaim']['claimName'])
 if claim is None:sys.exit('original native cache claim missing')
 cache=d['historical_crl_caches'].get(claim['metadata']['uid']+':'+path)
 if cache is None or cache['uid']!=10001 or cache['mode']!='600':sys.exit('original native private cache missing')
 info=os.lstat(cache['file'])
 if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode)!=0o600:sys.exit('native cache is not a private regular file')
 with open(cache['file'],'rb') as source:sys.stdout.buffer.write(source.read())
else:os.execv(os.environ['RTK_HISTORICAL_CRL_ORIGINAL'],[os.environ['RTK_HISTORICAL_CRL_ORIGINAL']]+a)
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_HISTORICAL_CRL_ORIGINAL", original)
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if inventory, err := f.backend.Inventory(t.Context(), f.p.Plan.Stack); err != nil || len(inventory) == 0 {
		t.Fatal("historical CRL adapter cannot read its complete native inventory", err)
	}
	for _, name := range []string{"certissuer", "pki-controller"} {
		id := "Deployment/" + f.p.Plan.Stack + "-video-cloud/" + name
		object, err := f.backend.Read(t.Context(), id)
		if err != nil || object == nil || deploymentplan.ID(object) != id {
			t.Fatal("historical CRL adapter cannot point-read its native TLS consumer", name, err)
		}
	}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		id := "Deployment/" + f.p.Plan.Stack + "-video-cloud/pki-export-" + name + "-" + f.e.Journal.PlanSHA256[:12]
		object, err := f.backend.Read(t.Context(), id)
		if err != nil || object != nil {
			t.Fatal("historical CRL adapter cannot prove its retired API reader absent", name, err)
		}
	}
}

func historicalNativeSignedCRL(t *testing.T, issuer environmentCreateRegisteredIssuer, key *ecdsa.PrivateKey, numbers ...int64) managedLateCRLRecord {
	t.Helper()
	cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
	if err != nil || key == nil {
		t.Fatal("original CRL signing authority unavailable", issuer.Domain, err)
	}
	number := int64(1)
	if len(numbers) == 1 {
		number = numbers[0]
	}
	if len(numbers) > 1 || number < 1 {
		t.Fatal("invalid original native CRL generation")
	}
	now := time.Now().UTC()
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(number), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour)}, cert, key)
	if err != nil {
		t.Fatal("native CRL signing", err)
	}
	parsed, err := x509.ParseRevocationList(der)
	if err != nil {
		t.Fatal(err)
	}
	record := managedLateCRLRecord{IssuerID: issuer.ID, Digest: environmentPreflightBytesSHA256(der), Number: parsed.Number.String(), PEM: string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})), ThisUpdate: parsed.ThisUpdate, NextUpdate: parsed.NextUpdate}
	if _, err := validateManagedLateSignedCRL(issuer.Domain, issuer.ID, environmentPreflightBytesSHA256(cert.Raw), issuer.Status, issuer.ChainPEM, record, managedLateObserveACKReady, now); err != nil {
		t.Fatal("native CRL signature did not match actual issuer", err)
	}
	return record
}

func publishHistoricalNativeCRL(t *testing.T, f *acceptedCoreExecutionContract, record createRootCRLRecord) {
	t.Helper()
	records, ok := f.protocol["historical_crls"].(map[string]managedLateCRLRecord)
	if !ok || records[record.IssuerID].IssuerID != record.IssuerID {
		t.Fatal("native import selected an unknown original authority")
	}
	doc := deploymentplan.Map(f.protocol["issuers"].(map[string]any)[record.IssuerID])
	issuer, err := environmentCreateRegisteredIssuerFromMap(doc, "")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	current := managedLateCRLRecord{record.IssuerID, record.Digest, record.Number, record.PEM, record.ThisUpdate, record.NextUpdate}
	if _, err := validateManagedLateSignedCRL(issuer.Domain, issuer.ID, environmentPreflightBytesSHA256(cert.Raw), issuer.Status, issuer.ChainPEM, current, managedLateObserveACKReady, time.Now().UTC()); err != nil {
		t.Fatal("native import changed its original signer", err)
	}
	records[record.IssuerID] = current
	history, ok := f.protocol["historical_crl_history"].(map[string][]managedLateCRLRecord)
	if !ok || len(history[record.IssuerID]) == 0 {
		t.Fatal("native CRL history unavailable for original authority")
	}
	history[record.IssuerID] = append(history[record.IssuerID], current)
	f.publish(t)
}

// The retained controllers automatically publish a same-key ICA successor
// before the one-hour original CRL can fall below the later completion budget.
// Model that native event without changing the immutable Root#2 artifact: the
// registry history grows monotonically and both durable consumers install and
// acknowledge the exact new signed bytes.
func publishHistoricalOpenBaoTLSICASuccessor(t *testing.T, f *deviceTrustExecutionContract, ica environmentCreateRegisteredIssuer) createRootCRLRecord {
	t.Helper()
	native := historicalNativeSignedCRL(t, ica, f.authorityKeys[ica.ID], 4)
	record := createRootCRLRecord(native)
	records, recordsOK := f.protocol["historical_crls"].(map[string]managedLateCRLRecord)
	history, historyOK := f.protocol["historical_crl_history"].(map[string][]managedLateCRLRecord)
	cert, certErr := parseFirstCertificatePEM([]byte(ica.Certificate))
	if !recordsOK || !historyOK || records[ica.ID].IssuerID != ica.ID || len(history[ica.ID]) == 0 || certErr != nil {
		t.Fatal("native current ICA registry history unavailable", certErr)
	}
	if _, err := validateManagedLateSignedCRL(ica.Domain, ica.ID, environmentPreflightBytesSHA256(cert.Raw), ica.Status, ica.ChainPEM, native, managedLateObserveACKReady, time.Now().UTC()); err != nil {
		t.Fatal("automatic current ICA successor changed its original signer", err)
	}
	records[ica.ID] = native
	history[ica.ID] = append(history[ica.ID], native)
	caches, ok := f.protocol["historical_crl_caches"].(map[string]map[string]any)
	if !ok {
		t.Fatal("native current ICA caches unavailable")
	}
	updated := 0
	seenCacheIssuers := map[string]int{}
	for _, cache := range caches {
		file, _ := cache["file"].(string)
		var body struct {
			IssuerFingerprint string              `json:"issuer_fingerprint"`
			CRL               createRootCRLRecord `json:"crl"`
		}
		if file == "" {
			t.Fatal("native current ICA cache path unavailable")
		}
		raw, err := os.ReadFile(file)
		if err != nil || decodeEnvironmentJSON(raw, &body) != nil {
			t.Fatal("native current ICA cache cannot be decoded", err)
		}
		seenCacheIssuers[body.CRL.IssuerID]++
		if body.CRL.IssuerID != ica.ID {
			continue
		}
		body.CRL = record
		if err := os.WriteFile(file, mustProviderRecoveryJSON(body), 0600); err != nil {
			t.Fatal(err)
		}
		cache["digest"] = record.Digest
		updated++
	}
	acks, ok := f.protocol["historical_crl_acks"].([]managedLatePriorACK)
	if !ok {
		t.Fatal("native current ICA acknowledgments unavailable")
	}
	now, acknowledged := time.Now().UTC(), 0
	consumers := map[string]bool{}
	for index := range acks {
		if acks[index].IssuerID != ica.ID {
			continue
		}
		acks[index].Digest, acks[index].At = record.Digest, now
		consumers[acks[index].Consumer] = true
		acknowledged++
	}
	if updated != 2 || acknowledged != 2 || !consumers["certissuer"] || !consumers["pki-controller"] {
		t.Fatalf("automatic current ICA successor requires two exact native consumers: ICA=%s caches=%d seen=%v ACKs=%d consumers=%v", ica.ID, updated, seenCacheIssuers, acknowledged, consumers)
	}
	f.protocol["historical_crl_acks"] = acks
	f.publish(t)
	return record
}

// Simulate the native consumers only once their actual retained controller,
// owned Pod, manifest mount and original durable claim exist. Cache writes and
// ACK timestamps happen now while the signed original publication is valid.
// Later expiry is observed without changing files, signatures or old ACK times.
func publishHistoricalPriorCRLConsumerCaches(t *testing.T, f *deviceTrustExecutionContract, roots, intermediates map[string]environmentCreateRegisteredIssuer) {
	t.Helper()
	if err := writeHistoricalPriorCRLConsumerCaches(f, roots, intermediates, t.TempDir(), []string{"mqtt", "openbao_tls"}); err != nil {
		t.Fatal(err)
	}
	f.publish(t)
	installHistoricalLogIdentityProtocol(t, f)
}

// The error-returning native process boundary allows a small ordering test to
// verify that missing original mounts/manifests cannot produce any receipt.
func writeHistoricalPriorCRLConsumerCaches(f *deviceTrustExecutionContract, roots, intermediates map[string]environmentCreateRegisteredIssuer, dir string, domains []string) error {
	objects := historicalNativeInventory(f.native.objects)
	records, ok := f.protocol["historical_crls"].(map[string]managedLateCRLRecord)
	if !ok {
		return fmt.Errorf("native original CRL registry unavailable")
	}
	caches := f.protocol["historical_crl_caches"].(map[string]map[string]any)
	acks := f.protocol["historical_crl_acks"].([]managedLatePriorACK)
	ns := f.p.Plan.Stack + "-video-cloud"
	for _, domain := range domains {
		root := roots[domain]
		names := []string{"video-cloud-api", "video-cloud-logingester"}
		if domain == "openbao_tls" {
			names = []string{"certissuer", "pki-controller"}
		}
		for _, name := range names {
			id := "Deployment/" + ns + "/" + name
			controller := objects[id]
			if controller == nil || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(deploymentplan.Map(controller["metadata"])["uid"])) {
				return fmt.Errorf("original CRL consumer controller missing: %s", id)
			}
			container, volume, path, cmName := "app", "mqtt-bundle-state", "/var/lib/mqtt-trust/root-crl.json", "pki-create-mqtt-consumer-trust"
			if domain == "openbao_tls" {
				container, volume, path, cmName = name, "service-state", "/var/lib/pki-service/private/crl/openbao-root.json", "pki-create-openbao-tls-consumer-trust"
			}
			c := managedNamedField(environmentPKIPod(controller)["containers"], container)
			cm := objects["ConfigMap/"+ns+"/"+cmName]
			if c == nil || cm == nil {
				return fmt.Errorf("native original consumer or trust manifest missing: %s", id)
			}
			mounts, err := environmentPKINamedFields(c["volumeMounts"])
			volumes, volErr := environmentPKINamedFields(environmentPKIPod(controller)["volumes"])
			mount, vol := mounts[volume], volumes[volume]
			if err != nil || volErr != nil || mount == nil || vol == nil || mount["readOnly"] == true || mount["subPath"] != nil || !strings.HasPrefix(path, deploymentplan.String(mount["mountPath"])+"/") {
				return fmt.Errorf("original cache durable mount differs: %s", id)
			}
			claim := objects["PersistentVolumeClaim/"+ns+"/"+deploymentplan.String(deploymentplan.Map(vol["persistentVolumeClaim"])["claimName"])]
			claimUID := deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"])
			if !environmentBootstrapOwnerUUID.MatchString(claimUID) {
				return fmt.Errorf("native original cache claim missing: %s", id)
			}
			pods := 0
			for _, pod := range objects {
				if pod["kind"] != "Pod" {
					continue
				}
				owner, err := environmentPodController(pod, objects)
				if err == nil && owner == id {
					pods++
					if !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])) {
						return fmt.Errorf("native cache Pod UID invalid: %s", id)
					}
				}
			}
			if pods != 1 {
				return fmt.Errorf("CRL consumer must have one actual current owned Pod: %s count=%d", id, pods)
			}
			issuers := []environmentCreateRegisteredIssuer{root}
			if domain == "openbao_tls" {
				if validateOpenBaoTLSRootCRLManifest(deploymentplan.Resource{Desired: deploymentplan.Projection(cm)}, root, intermediates[domain]) != nil {
					return fmt.Errorf("native original TLS manifest differs")
				}
				issuers = append(issuers, intermediates[domain])
			} else {
				file, err := coreMQTTFile(controller, c, "mqtt-pki-crls", "crls.json")
				if err != nil || file.reference != cmName || file.file != mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST") {
					return fmt.Errorf("native original MQTT CRL manifest differs: %v", err)
				}
			}
			for i, issuer := range issuers {
				statePath := path
				if i == 1 {
					statePath = strings.TrimSuffix(path, ".json") + "-" + issuer.ID + ".json"
				}
				key := claimUID + ":" + statePath
				record := records[issuer.ID]
				if existing := caches[key]; existing != nil {
					if existing["digest"] != record.Digest {
						return fmt.Errorf("original native cache was replaced without import")
					}
					continue
				}
				cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
				now := time.Now().UTC()
				if err != nil {
					return err
				}
				fingerprint := environmentPreflightBytesSHA256(cert.Raw)
				if _, err := validateManagedLateSignedCRL(issuer.Domain, issuer.ID, fingerprint, issuer.Status, issuer.ChainPEM, record, managedLateObserveACKReady, now); err != nil {
					return fmt.Errorf("original native cache installation requires currently valid signed CRL: %s: %w", issuer.Domain, err)
				}
				file := filepath.Join(dir, deploymentplan.Digest(key)+".json")
				body := mustProviderRecoveryJSON(map[string]any{"issuer_fingerprint": fingerprint, "crl": record})
				if err := os.WriteFile(file, body, 0600); err != nil {
					return err
				}
				caches[key] = map[string]any{"file": file, "uid": 10001, "mode": "600", "digest": record.Digest}
				acks = append(acks, managedLatePriorACK{IssuerID: issuer.ID, Digest: record.Digest, Consumer: name, At: now})
			}
		}
	}
	f.protocol["objects"], f.protocol["historical_crl_acks"] = objects, acks
	return nil
}
