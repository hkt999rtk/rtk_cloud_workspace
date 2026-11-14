package main

// Retain the existing environment's MQTT, App, OpenBao and device recovery
// state while switching only the admitted same-key Service session lineage.
func adoptCompletedServiceSession(current, latest *createServiceSessionRecovery) error {
	if current == nil || latest == nil || current.ManagedTransportRecovery == nil || latest.ManagedTransportRecovery == nil || latest.ManagedTransportRecovery.SessionRenewal == nil || !latest.SecondSession || !latest.Renewal || current.Ceremony.Root.RootFingerprint != latest.Ceremony.Root.RootFingerprint || current.Ceremony.Intermediate.ID != latest.Ceremony.Intermediate.ID || current.OriginalKeySHA != latest.OriginalKeySHA || current.OriginalCSRSHA != latest.OriginalCSRSHA {
		return createProviderRecoveryBlock("completed Service session does not preserve the original identity lineage")
	}
	current.Ceremony, current.Intent, current.RenewedIntent = latest.Ceremony, latest.Intent, latest.RenewedIntent
	current.OriginalKeySHA, current.OriginalCSRSHA = latest.OriginalKeySHA, latest.OriginalCSRSHA
	current.Review, current.RenewalBindingSHA = latest.Review, latest.RenewalBindingSHA
	current.SourceRenewalStage, current.RenewedCertPath = latest.SourceRenewalStage, latest.RenewedCertPath
	current.Renewal, current.PostServiceRenewal, current.SecondSession = true, true, true
	current.RenewalCandidate = latest.RenewalCandidate
	current.qualifiedPlan = latest.qualifiedPlan
	current.ManagedTransportRecovery.PartialSessionRenewal = true
	current.ManagedTransportRecovery.SessionRenewal = latest.ManagedTransportRecovery.SessionRenewal
	current.ManagedTransportRecovery.PartialTrustForward = nil
	current.ManagedTransportRecovery.PartialTrustForwardReview = ""
	return nil
}
