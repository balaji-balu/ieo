package handlers

import (
	"context"
	"fmt"
	"log"

	"github.com/balaji-balu/ieo/ent"
	"github.com/balaji-balu/ieo/pkg/application"
)

func Persist(ctx context.Context, client *ent.Client, localAppName, category string, ad *application.ApplicationDescription) error {
	//ad := ads[0]
	log.Println("[CO] persisting app desc 1:", ad)
	//return nil

	// save catlog info
	appcreate := client.ApplicationDesc.
		Create().
		SetAppID(ad.Metadata.ID).
		SetName(localAppName).
		SetVersion(ad.Metadata.Version)
		//SetVendor(ad.Metadata.Catalog.Application.Site).
		//SetDescription(ad.Metadata.Catalog.Application.DescriptionFile)
		//Save(ctx)
		if category != "" {
			appcreate.SetCategory(category)
		}
		desc := ad.Metadata.Description
		if desc != ""{
			appcreate.SetDescription(desc)
		}
		orgs := ad.Metadata.Catalog.Organization
		if orgs != nil {
			appcreate.SetVendor(ad.Metadata.Catalog.Organization[0].Name)
		}
		app := ad.Metadata.Catalog.Application
		if app != nil { 
			if app.Tags != nil {
				appcreate.SetTags(ad.Metadata.Catalog.Application.Tags)
			}
			if app.Tagline != "" {
				appcreate.SetTagLine(ad.Metadata.Catalog.Application.Tagline)
			}
			if app.Icon != "" {
				appcreate.SetIcon(ad.Metadata.Catalog.Application.Icon)
			}
			if app.Site != "" {
				appcreate.SetCategory(ad.Metadata.Catalog.Application.Site).
						 SetSite(ad.Metadata.Catalog.Application.Site)
			}	
		}
	appObj, err := appcreate.Save(ctx)	
	if err != nil {	
		return err
	}
	appID := appObj.ID
	
	
	// // Store
	log.Println("number of dep profiles: ", len(ad.DeploymentProfiles))

	for _, spec := range ad.DeploymentProfiles {

		dpCreate := client.DeploymentProfile.
			Create().
			//SetID(spec.ID).
			SetType(spec.Type).
			SetAppID(appID)

		if spec.Description != "" {
			dpCreate.SetDescription(spec.Description)
		}
		log.Println("ad.RequiredResources", spec.RequiredResources)
		if spec.RequiredResources != nil {
			log.Println("CPU Cores:", spec.RequiredResources.CPU.Cores)
			// for _, p := range ad.RequiredResources.Peripherals {
			//     log.Println("Peripheral:", p)
			// }
			dpCreate.
				SetCPUCores(spec.RequiredResources.CPU.Cores).
				SetMemory(spec.RequiredResources.Memory).
				SetStorage(spec.RequiredResources.Storage).
				SetCPUArchitectures(spec.RequiredResources.CPU.Architectures).
				SetPeripherals(peripheralsToMap(spec.RequiredResources.Peripherals)).
				SetInterfaces(interfacesToMap(spec.RequiredResources.Interfaces))
		} else {
			log.Println("Skipping RequiredResources, it is nil")
			//continue
		}
		dpObj, err := dpCreate.Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create deployment profile: %w", err)
		}
		dpid := dpObj.ID

		// components
		for _, component := range spec.Components {
			_, err := client.Component.
				Create().
				SetName(component.Name).
				SetDeploymentProfileID(dpid).
				SetProperties(application.ComponentProperties{
					Repository:      component.Properties.Repository,
					Revision:        component.Properties.Revision,
					Wait:            component.Properties.Wait,
					Timeout:         component.Properties.Timeout,
					PackageLocation: component.Properties.PackageLocation,
					KeyLocation:     component.Properties.KeyLocation,
				}).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to create component: %w", err)
			}
		}
	}

	return nil
}

func peripheralsToMap(peripherals []application.Peripheral) []map[string]interface{} {
	result := make([]map[string]interface{}, len(peripherals))
	for i, p := range peripherals {
		result[i] = map[string]interface{}{
			"manufacturer": p.Manufacturer,
			"type":         p.Type,
			"model":        p.Model,
			// add more fields here if needed
		}
	}
	return result
}

func interfacesToMap(interfaces []application.Interface) []map[string]interface{} {
	result := make([]map[string]interface{}, len(interfaces))
	for i, iface := range interfaces {
		result[i] = map[string]interface{}{
			"type": iface.Type,
			//"protocol": iface.Protocol,
			// add more fields here if needed
		}
	}
	return result
}
