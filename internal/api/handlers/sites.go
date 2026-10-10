package handlers

import (
	//"encoding/json"
    "github.com/google/uuid"
	"github.com/gin-gonic/gin"

	"github.com/balaji-balu/ieo/ent"
    "github.com/balaji-balu/ieo/ent/site"
    "github.com/balaji-balu/ieo/ent/host"

)
func ListSites(c *gin.Context, client *ent.Client) {
	sites, err := client.Site.
		Query().
		All(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, sites)
}

func GetSiteInfo(c *gin.Context, client *ent.Client) {
    ctx := c.Request.Context()
	siteId := c.Param("siteId")
    sid, err := uuid.Parse(siteId)
    if err != nil {
        c.JSON(400, gin.H{"error": "invalid site_id"})
        return
    }

	site, err := client.Site.
		Query().
		Where(site.SiteID(sid)).
		//WithHosts().
		Only(ctx)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
    hosts, _ := client.Host.
        Query().
        Where(host.SiteIDEQ(sid)).
        All(ctx)

    c.JSON(200, gin.H{
    "site": site,
    "hosts": hosts,
})


}

type RegisterSiteRequest struct {
    ID   string `json:"id" binding:"required"`
    Name string `json:"name" binding:"required"`
}

func RegisterSite(c *gin.Context, client *ent.Client) {
    var req RegisterSiteRequest
    ctx := c.Request.Context()
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(400, gin.H{"error": "invalid body", "details": err.Error()})
        return
    }

    sid, err := uuid.Parse(req.ID)
    if err != nil {
        c.JSON(400, gin.H{"error": "invalid site_id"})
        return
    }
    existing, err := client.Site.
        Query().
        Where(site.SiteIDEQ(sid)).
        Only(ctx)

    if err != nil && !ent.IsNotFound(err) {
        // Some DB error (not "not found")
        c.JSON(500, gin.H{"error": "db query failed"})
        return
    }

    if existing != nil {
        // It exists → conflict
        c.JSON(409, gin.H{"error": "site already exists"})
        return
    }

    site, err := client.Site.
        Create().
        SetSiteID(sid).
        SetName(req.Name).
        Save(ctx)

    if err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    c.JSON(201, site)
}

type RegisterHostRequest struct {
    SiteID   string `json:"siteId" binding:"required"`
    Hostname string `json:"hostname" binding:"required"`
}

func RegisterHost(c *gin.Context, client *ent.Client) {
    hostId := c.Param("hostId")

    var req RegisterHostRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(400, gin.H{"error": "invalid body", "details": err.Error()})
        return
    }

    sid, err := uuid.Parse(req.SiteID)
    if err != nil {
        c.JSON(400, gin.H{"error": "invalid site_id"})
        return
    }
    
    // Verify that site exists
    _, err = client.Site.
        Query().
        Where(site.SiteIDEQ(sid)).
        Only(c.Request.Context())
    if err != nil {
        c.JSON(404, gin.H{"error": "site not found"})
        return
    }

    hid, err := uuid.Parse(hostId)
    if err != nil {
        c.JSON(400, gin.H{"error": "invalid host_id"})
        return
    }       
    host, err := client.Host.
        Create().
        SetHostID(hid).
        SetHostname(req.Hostname).
        SetSiteID(sid).
        Save(c.Request.Context())

    if err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    c.JSON(201, host)
}