package importer

func (s *state) dropTables(p *parser) {
	ifExists := false
	if p.acceptWord("IF") {
		p.acceptWord("EXISTS")
		ifExists = true
	}
	for {
		name, ok := p.qualifiedIdent()
		if !ok {
			if !ifExists {
				s.warn("DROP TABLE without a name; ignored")
			}
			return
		}
		if s.table(name) == nil {
			if !ifExists {
				s.warn("DROP TABLE %q: table does not exist; ignored", name)
			}
		} else {
			s.removeTable(name)
		}
		p.acceptWord("CASCADE")
		p.acceptWord("RESTRICT")
		if !p.acceptPunct(",") {
			return
		}
	}
}
