package api

import "net/http"

func (s *Server) dnsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/server", s.getServer)
	mux.HandleFunc("GET /api/dns/connections", s.listDNSConnections)
	mux.HandleFunc("POST /api/dns/connections", s.createDNSConnection)
	mux.HandleFunc("POST /api/dns/connections/{id}/check", s.checkDNSConnection)
	mux.HandleFunc("DELETE /api/dns/connections/{id}", s.deleteDNSConnection)
	mux.HandleFunc("GET /api/dns/zones", s.listDNSZones)
	mux.HandleFunc("GET /api/dns/zones/available", s.listAvailableDNSZones)
	mux.HandleFunc("PUT /api/dns/connections/{id}/zones/{zone}/managed", s.adoptDNSZone)
	mux.HandleFunc("DELETE /api/dns/connections/{id}/zones/{zone}/managed", s.releaseDNSZone)
	mux.HandleFunc("GET /api/dns/connections/{id}/zones/{zone}/records", s.listDNSRecords)
	mux.HandleFunc("PUT /api/dns/connections/{id}/zones/{zone}/records/{name}/{type}", s.putDNSRecord)
	mux.HandleFunc("DELETE /api/dns/connections/{id}/zones/{zone}/records/{name}/{type}", s.deleteDNSRecord)
	mux.HandleFunc("GET /api/dns/templates", s.listDNSTemplates)
	mux.HandleFunc("POST /api/dns/connections/{id}/zones/{zone}/templates/{template}", s.applyDNSTemplate)
	mux.HandleFunc("GET /api/sites/{name}/domains/status", s.domainStatuses)
	mux.HandleFunc("POST /api/sites/{name}/domains/{domain}/point", s.pointDomain)
}

func (s *Server) getServer(w http.ResponseWriter, r *http.Request) {
	ips, err := s.Panel.ServerIPs(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"ips": ips})
}

func (s *Server) listDNSConnections(w http.ResponseWriter, r *http.Request) {
	list, err := s.Panel.DNSConnections(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createDNSConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
		Token    string `json:"token"`
	}
	if !decodeJSON(w, r, &body, `{"provider": "hetzner", "name": "…", "token": "…"}`) {
		return
	}
	c, err := s.Panel.CreateDNSConnection(r.Context(), actorOf(r), body.Provider, body.Name, body.Token)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) checkDNSConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := s.Panel.CheckDNSConnection(r.Context(), actorOf(r), id)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) deleteDNSConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Panel.DeleteDNSConnection(r.Context(), actorOf(r), id); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listDNSZones(w http.ResponseWriter, r *http.Request) {
	zones, err := s.Panel.DNSZones(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, zones)
}

func (s *Server) listAvailableDNSZones(w http.ResponseWriter, r *http.Request) {
	zones, err := s.Panel.AvailableDNSZones(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, zones)
}

func (s *Server) adoptDNSZone(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	z, err := s.Panel.AdoptDNSZone(r.Context(), actorOf(r), id, r.PathValue("zone"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, z)
}

func (s *Server) releaseDNSZone(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Panel.ReleaseDNSZone(r.Context(), actorOf(r), id, r.PathValue("zone")); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listDNSRecords(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	list, err := s.Panel.DNSRecords(r.Context(), actorOf(r), id, r.PathValue("zone"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) putDNSRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		TTL    *int     `json:"ttl"`
		Values []string `json:"values"`
	}
	if !decodeJSON(w, r, &body, `{"ttl": 3600, "values": ["203.0.113.5"]}`) {
		return
	}
	rs, err := s.Panel.SetDNSRecord(r.Context(), actorOf(r), id, r.PathValue("zone"), r.PathValue("name"), r.PathValue("type"), body.TTL, body.Values)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

func (s *Server) deleteDNSRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Panel.DeleteDNSRecord(r.Context(), actorOf(r), id, r.PathValue("zone"), r.PathValue("name"), r.PathValue("type")); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) domainStatuses(w http.ResponseWriter, r *http.Request) {
	list, err := s.Panel.DomainStatuses(r.Context(), actorOf(r), r.PathValue("name"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) pointDomain(w http.ResponseWriter, r *http.Request) {
	res, err := s.Panel.PointDomain(r.Context(), actorOf(r), r.PathValue("name"), r.PathValue("domain"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) listDNSTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := s.Panel.DNSTemplates(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// applyDNSTemplate returns what the template changes; with confirm the
// changes are written.
func (s *Server) applyDNSTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Params  map[string]string `json:"params"`
		Confirm bool              `json:"confirm"`
	}
	// An empty body is {"params": {}, "confirm": false}.
	if r.ContentLength != 0 && !decodeJSON(w, r, &body, `{"params": {"value": "…"}, "confirm": false}`) {
		return
	}
	res, err := s.Panel.ApplyDNSTemplate(r.Context(), actorOf(r), id, r.PathValue("zone"), r.PathValue("template"), body.Params, body.Confirm)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
