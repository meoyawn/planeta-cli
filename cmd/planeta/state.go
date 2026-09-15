package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

type appPaths struct {
	auth   string
	config string
	index  string
	legacy string
}

func defaultPaths() (appPaths, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return appPaths{}, fmt.Errorf("locate user config directory: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return appPaths{}, fmt.Errorf("locate user cache directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return appPaths{}, fmt.Errorf("locate home directory: %w", err)
	}
	return appPaths{
		auth:   filepath.Join(config, "planeta", "auth.json"),
		config: filepath.Join(config, "planeta", "config.json"),
		index:  filepath.Join(cache, "planeta", "products"),
		legacy: filepath.Join(home, ".config"),
	}, nil
}

type productLocation struct {
	ID   string `json:"id"`
	City string `json:"city"`
	URL  string `json:"url"`
}

func (p appPaths) remember(city string, products []product) error {
	base, err := url.Parse(siteOrigin)
	if err != nil {
		return fmt.Errorf("parse catalog origin: %w", err)
	}
	for _, item := range products {
		if _, err := validateProductURL(item.URL, base, city, item.ID); err != nil {
			return err
		}
		path := filepath.Join(p.index, city, item.ID+".json")
		if err := atomicJSON(path, productLocation{ID: item.ID, City: city, URL: item.URL}); err != nil {
			return fmt.Errorf("remember product %s URL: %w", item.ID, err)
		}
	}
	return nil
}

func (p appPaths) lookup(city, id string) (string, error) {
	if !citySlug.MatchString(city) || !validID.MatchString(id) {
		return "", fmt.Errorf("invalid city %q or product id %q", city, id)
	}
	data, err := os.ReadFile(filepath.Join(p.index, city, id+".json")) // #nosec G304 G703 -- City and ID are validated above; p.index is the local CLI cache directory.
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no URL saved for product %s in %s; run planeta search first, or use planeta id --url <canonical-product-url> %s", id, city, id)
	}
	if err != nil {
		return "", fmt.Errorf("read product %s URL: %w", id, err)
	}
	var item productLocation
	if json.Unmarshal(data, &item) != nil || item.ID != id || item.City != city {
		return "", fmt.Errorf("invalid saved URL for product %s; repeat planeta search to refresh it", id)
	}
	base, err := url.Parse(siteOrigin)
	if err != nil {
		return "", fmt.Errorf("parse catalog origin: %w", err)
	}
	if _, err := validateProductURL(item.URL, base, city, id); err != nil {
		return "", err
	}
	return item.URL, nil
}
