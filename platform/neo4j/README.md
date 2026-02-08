# Neo4j

Graph database for knowledge graphs and relationship-based queries.

**Status:** Accepted | **Updated:** 2026-02-07

---

## Overview

Neo4j provides graph database capabilities for knowledge graphs, relationship traversal, and graph-enhanced RAG.

```mermaid
flowchart LR
    subgraph Neo4j["Neo4j"]
        Core[Neo4j Core]
        Cypher[Cypher Engine]
        GDS[Graph Data Science]
    end

    subgraph Use Cases
        KG[Knowledge Graph]
        RAG[Graph RAG]
        Fraud[Fraud Detection]
    end

    App[Application] --> Cypher
    Cypher --> Core
    Core --> GDS
    Neo4j --> Use Cases
```

---

## Why Neo4j?

| Feature | Benefit |
|---------|---------|
| Native graph storage | Optimized for relationships |
| Cypher query language | Intuitive graph queries |
| Graph Data Science | ML on graphs |
| ACID transactions | Enterprise reliability |
| Knowledge graphs | Document relationships |

---

## Use Cases in AI Hub

| Use Case | Description |
|----------|-------------|
| **Document relationships** | SUPERSEDES, AMENDS, REFERENCES |
| **Graph RAG** | Traverse related documents |
| **Entity extraction** | Store extracted entities |
| **Taxonomy** | Topic hierarchies |

---

## Configuration

### Helm Values

```yaml
neo4j:
  name: neo4j
  edition: community  # or enterprise

  resources:
    cpu: "2"
    memory: "4Gi"

  volumes:
    data:
      mode: defaultStorageClass
      size: 50Gi

  config:
    dbms.memory.heap.initial_size: "2G"
    dbms.memory.heap.max_size: "2G"
    dbms.memory.pagecache.size: "1G"

  services:
    neo4j:
      spec:
        type: ClusterIP
```

---

## Knowledge Graph Schema

### Nodes

```cypher
// Document node
CREATE (d:Document {
  document_id: "doc-001",
  circular_number: "BSD/2024/001",
  title: "Anti-Money Laundering Guidelines",
  effective_date: date("2024-01-15"),
  status: "active",
  summary: "..."
})

// Topic node
CREATE (t:Topic {
  name: "AML",
  category: "Compliance"
})
```

### Relationships

```cypher
// Document relationships
(d1:Document)-[:SUPERSEDES]->(d2:Document)
(d1:Document)-[:AMENDS]->(d2:Document)
(d1:Document)-[:REFERENCES]->(d2:Document)
(d:Document)-[:HAS_TOPIC]->(t:Topic)
(d:Document)-[:HAS_ATTACHMENT]->(a:Attachment)
```

---

## Graph RAG Queries

### Find Active Version

```cypher
MATCH (d:Document {circular_number: $circular_number})
WHERE NOT EXISTS((d)<-[:SUPERSEDES]-())
RETURN d
```

### Get Related Documents

```cypher
MATCH (d:Document {document_id: $doc_id})
OPTIONAL MATCH (d)-[:REFERENCES]->(ref:Document)
OPTIONAL MATCH (d)-[:SUPERSEDES]->(old:Document)
OPTIONAL MATCH (d)<-[:AMENDS]-(amendment:Document)
RETURN d, collect(DISTINCT ref) as references,
       collect(DISTINCT old) as superseded,
       collect(DISTINCT amendment) as amendments
```

### Topic-based Retrieval

```cypher
MATCH (d:Document)-[:HAS_TOPIC]->(t:Topic {name: $topic})
WHERE d.status = 'active'
RETURN d
ORDER BY d.effective_date DESC
LIMIT 10
```

---

## Graph Data Science

### Similarity Analysis

```cypher
CALL gds.graph.project(
  'documents',
  'Document',
  {
    REFERENCES: {orientation: 'UNDIRECTED'},
    HAS_TOPIC: {orientation: 'UNDIRECTED'}
  }
)

CALL gds.nodeSimilarity.stream('documents')
YIELD node1, node2, similarity
RETURN gds.util.asNode(node1).title AS doc1,
       gds.util.asNode(node2).title AS doc2,
       similarity
ORDER BY similarity DESC
LIMIT 10
```

---

## Python Integration

```python
from neo4j import GraphDatabase

driver = GraphDatabase.driver(
    "bolt://neo4j.ai-hub.svc:7687",
    auth=("neo4j", password)
)

def get_document_context(document_id: str) -> dict:
    with driver.session() as session:
        result = session.run("""
            MATCH (d:Document {document_id: $doc_id})
            OPTIONAL MATCH (d)-[:SUPERSEDES]->(old:Document)
            OPTIONAL MATCH (d)-[:REFERENCES]->(ref:Document)
            RETURN d, collect(old) as superseded, collect(ref) as references
        """, doc_id=document_id)
        return result.single()
```

---

## Monitoring

| Metric | Query |
|--------|-------|
| Query count | `neo4j_database_query_execution_total` |
| Query latency | `neo4j_database_query_execution_latency` |
| Store size | `neo4j_database_store_size_total` |
| Node count | `neo4j_database_count_node` |

---

## Backup

```bash
# Neo4j backup via neo4j-admin
neo4j-admin database dump neo4j --to-path=/backups/

# Restore
neo4j-admin database load neo4j --from-path=/backups/neo4j.dump
```

---

## Consequences

**Positive:**
- Native graph storage
- Intuitive Cypher queries
- Graph RAG enhancement
- ACID transactions
- Graph algorithms (GDS)

**Negative:**
- Additional infrastructure
- Learning curve for Cypher
- Memory-intensive for large graphs

---

*Part of [OpenOva](https://openova.io)*
