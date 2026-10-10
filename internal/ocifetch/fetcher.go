package ocifetch

import (
    "context"
    "fmt"
    "oras.land/oras-go/v2"
    "oras.land/oras-go/v2/content/oci"
    "oras.land/oras-go/v2/registry/remote"
    "oras.land/oras-go/v2/registry/remote/auth"
)

type Fetcher struct {
    Image  string
    Tag    string
    Token  string
}

func (f *Fetcher) Fetch(ctx context.Context) error {
    repo, err := remote.NewRepository(f.Image)
    if err != nil {
        return fmt.Errorf("invalid repo: %w", err)
    }

    repo.Client = &auth.Client{
        Credential: auth.StaticCredential(repo.Reference.Registry, auth.Credential{
            Username: "balaji",
            Password: f.Token,
        }),
        Cache: auth.NewCache(),
    }

    store, err := oci.New("local-cache")
    if err != nil {
        return fmt.Errorf("failed to create oci store: %w", err)
    }

    _, err = oras.Copy(ctx, repo, f.Tag, store, "", oras.DefaultCopyOptions)
    if err != nil {
        return fmt.Errorf("oras copy failed: %w", err)
    }

    return nil
}
