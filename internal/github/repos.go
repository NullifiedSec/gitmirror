package github

import (
	"context"
	"encoding/json"
)

type Repository struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	HooksURL string `json:"hooks_url"`
}

func (c *Client) UserRepositories(ctx context.Context) ([]Repository, error) {
	body, _, err := c.Get(ctx, "/user/repos?per_page=100")
	if err != nil {
		return nil, err
	}

	var repos []Repository
	if err := json.Unmarshal(body, &repos); err != nil {
		return nil, err
	}
	return repos, nil
}
