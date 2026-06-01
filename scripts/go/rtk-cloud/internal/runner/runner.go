package runner

type Spec struct {
	HostLabel   string
	RunnerName  string
	Repo        string
	Type        string
	CustomLabel string
}

func Specs() []Spec {
	const host = "rtk-shared-linux-ci"
	const typ = "g6-standard-4"
	return []Spec{
		{host, "rtk-ci-account-manager", "hkt999rtk/rtk_account_manager", typ, "account-manager-ci"},
		{host, "rtk-ci-cloud-admin", "hkt999rtk/rtk_cloud_admin", typ, "rtk-cloud-admin-ci"},
		{host, "rtk-ci-cloud-frontend", "hkt999rtk/rtk_cloud_frontend", typ, "rtk_cloud_frontend,go"},
		{host, "rtk-ci-cloud-client-linux", "hkt999rtk/rtk_cloud_client", typ, "client-sdk-ci"},
		{host, "rtk-ci-cloud-logger", "hkt999rtk/rtk_cloud_logger", typ, "rtk-cloud-logger-ci"},
	}
}
