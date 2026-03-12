package models

type DeploymentStatusResp struct {
	DeploymentID string   `json:"deployment_id"`
	Status       string   `json:"status"`
	Logs         []string `json:"logs"`
	LiveURL      string   `json:"live_url"`
}
