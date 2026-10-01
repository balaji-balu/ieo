package initflow

func Run(ctx *Context) error {
	steps := []Step{
		DiscoverEnvironment,
		//EnsureBinaries,
		GenerateAllConfigs,
		//GenerateConfig,
		// BootstrapGit,
		// StartServices,
		// RegisterSite,
		// Verify,
	}

	for _, step := range steps {
		if err := step(ctx); err != nil {
			return err
		}
	}

	return nil
}
/*
$ edgectl status

CO    Running   v0.1.15   port=8080  pid=1234
LO    Running   v0.1.15   node=bala-edge-01  pid=1240
EN    Running   v0.1.15   runtime=containerd pid=1245

System   ✔ Healthy
*/
