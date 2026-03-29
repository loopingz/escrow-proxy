package archive

import (
	"context"
	"fmt"
	"strings"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote"
)

// PushToRegistry pushes an OCI layout directory to a remote registry reference.
func PushToRegistry(ctx context.Context, layoutDir, ref string) error {
	store, err := oci.New(layoutDir)
	if err != nil {
		return fmt.Errorf("opening OCI layout: %w", err)
	}

	repo, err := remote.NewRepository(ref)
	if err != nil {
		return fmt.Errorf("creating remote repo: %w", err)
	}

	// ref contains "host/repo:tag"; extract the tag as the local resolve target
	tag := ref
	if idx := strings.LastIndex(ref, ":"); idx != -1 {
		tag = ref[idx+1:]
	}
	desc, err := store.Resolve(ctx, tag)
	if err != nil {
		return fmt.Errorf("resolving local manifest: %w", err)
	}

	if _, err := oras.Copy(ctx, store, desc.Digest.String(), repo, ref, oras.DefaultCopyOptions); err != nil {
		return fmt.Errorf("pushing to registry: %w", err)
	}

	return nil
}

// PullFromRegistry pulls an OCI layout from a remote registry reference into destDir.
func PullFromRegistry(ctx context.Context, ref, destDir string) error {
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return fmt.Errorf("creating remote repo: %w", err)
	}

	store, err := oci.New(destDir)
	if err != nil {
		return fmt.Errorf("creating OCI layout: %w", err)
	}

	desc, err := repo.Resolve(ctx, ref)
	if err != nil {
		return fmt.Errorf("resolving remote ref: %w", err)
	}

	if _, err := oras.Copy(ctx, repo, desc.Digest.String(), store, "", oras.DefaultCopyOptions); err != nil {
		return fmt.Errorf("pulling from registry: %w", err)
	}

	return nil
}
