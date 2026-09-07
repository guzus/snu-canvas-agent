package canvas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

func (c *Client) GetFiles(ctx context.Context, courseID int) ([]File, error) {
	params := url.Values{
		"sort":     {"updated_at"},
		"order":    {"desc"},
		"per_page": {"50"},
	}

	raw, err := c.getPaginated(ctx, fmt.Sprintf("/courses/%d/files", courseID), params)
	if err != nil {
		return nil, err
	}

	var files []File
	for _, r := range raw {
		var f File
		if err := json.Unmarshal(r, &f); err != nil {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

func (c *Client) GetModules(ctx context.Context, courseID int) ([]Module, error) {
	params := url.Values{
		"include[]": {"items"},
		"per_page":  {"50"},
	}

	raw, err := c.getPaginated(ctx, fmt.Sprintf("/courses/%d/modules", courseID), params)
	if err != nil {
		return nil, err
	}

	var modules []Module
	for _, r := range raw {
		var m Module
		if err := json.Unmarshal(r, &m); err != nil {
			continue
		}
		modules = append(modules, m)
	}
	return modules, nil
}

// GetFile fetches a single file's metadata within a course. This is the only
// way to reach files in courses whose Files tab is disabled: the module item
// gives a content_id, and this resolves it to a downloadable URL.
func (c *Client) GetFile(ctx context.Context, courseID, fileID int) (*File, error) {
	var f File
	if err := c.getAll(ctx, fmt.Sprintf("/courses/%d/files/%d", courseID, fileID), nil, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// GetFolders lists a course's folders so file paths can mirror the LMS layout.
func (c *Client) GetFolders(ctx context.Context, courseID int) ([]Folder, error) {
	params := url.Values{"per_page": {"100"}}

	raw, err := c.getPaginated(ctx, fmt.Sprintf("/courses/%d/folders", courseID), params)
	if err != nil {
		return nil, err
	}

	var folders []Folder
	for _, r := range raw {
		var f Folder
		if err := json.Unmarshal(r, &f); err != nil {
			continue
		}
		folders = append(folders, f)
	}
	return folders, nil
}

// GetModuleItems fetches a module's items from the dedicated endpoint. Canvas
// omits inline `items` from the modules list when a module holds many of them,
// so relying on the inline array alone silently loses files in exactly the
// large courses that need archiving most.
func (c *Client) GetModuleItems(ctx context.Context, courseID, moduleID int) ([]ModuleItem, error) {
	params := url.Values{"per_page": {"100"}}

	raw, err := c.getPaginated(ctx, fmt.Sprintf("/courses/%d/modules/%d/items", courseID, moduleID), params)
	if err != nil {
		return nil, err
	}

	var items []ModuleItem
	for _, r := range raw {
		var it ModuleItem
		if err := json.Unmarshal(r, &it); err != nil {
			continue
		}
		items = append(items, it)
	}
	return items, nil
}
