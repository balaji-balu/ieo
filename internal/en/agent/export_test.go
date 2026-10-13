package agent

// InventoryDue does what one `inventory.request` from the LO does, without the server between: a
// test that gives several reasons at once knows they have all arrived.
func (a *Agent) InventoryDue() { a.inventoryDue() }
