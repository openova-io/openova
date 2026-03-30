# CBO AI Sandbox — Ideation & Solution Thinking
**Internal Working Document | 2026-03-13**
**Status: Pre-proposal ideation — not final**

---

## FIRST: IS CBO DOING ML OR JUST LLM?

This is the most important question to settle before anything else.

**Answer: Both. And the Security Policy is the proof.**

The body of the RFP is ambiguous — it could be LLM experimentation only. But Appendix A (the Security Policy, written by actual humans) gives us five hard signals that they are doing **real ML model development**, not just prompt engineering:

| Signal | Why it points to ML |
|--------|-------------------|
| LIME and SHAP explicitly named | These are classical ML interpretability tools. You don't SHAP an LLM chat conversation — you SHAP a gradient boosting model or a neural network classifier. |
| "Bias in training data and resulting models" | LLMs are evaluated for output bias differently. Training data bias is a classical ML concern where you inspect feature distributions across protected groups. |
| "Model versioning: code, training data, hyperparameters" | Hyperparameter versioning is pure ML (learning rate, depth, epochs). LLMs don't have custom hyperparameters in a sandbox context. |
| "Fairness across different demographic groups" | This is a financial services ML concern — is our credit model fair to female applicants? To rural borrowers? Central banks do this for supervised models. |
| "Model drift detection" | Drift on a pretrained LLM is conceptually different. The classic drift problem is: "my fraud classifier trained in 2022 doesn't work on 2025 transaction patterns." |

**What CBO is most likely building:**
1. **Predictive/analytical ML models** — economic stress tests, risk indicators, forecasting models (Python-based, scikit-learn / XGBoost / PyTorch)
2. **LLM-powered tools** — document analysis, regulatory research assistants, summarization
3. **Possibly fine-tuned models** — a small Qwen or Mistral model fine-tuned on CBO's internal knowledge corpus (Arabic + English regulatory documents)

They are **not** building a foundation LLM from scratch. But they are absolutely doing supervised ML on tabular economic data.

---

## QUESTION-BY-QUESTION IDEATION

---

### "Develop and validate predictive models for economic stress testing" — what do we offer?

**What this actually is:** An economist uploads historical economic indicators (GDP growth, inflation, interest rates, credit defaults, bank capitalization ratios) and trains a model to predict outcomes under stress scenarios (e.g., "if oil prices drop 40%, what happens to bank NPL ratios?").

**The journey:**

```
Upload historical dataset (MinIO)
  ↓
Explore and visualize data (notebook environment)
  ↓
Feature engineering (pandas, numpy, statsmodels)
  ↓
Train model (scikit-learn, XGBoost, statsmodels, PyTorch)
  ↓
Track experiment (ML experiment tracker + model registry)
  ↓
Evaluate model (SHAP for feature importance, LIME for instance explanation)
  ↓
Assess fairness (bias toolkit)
  ↓
Register model version (model registry)
  ↓
Deploy as API endpoint (model serving framework)
  ↓
Monitor in production sandbox (observability + drift detection)
```

**What we provide:**
- **Notebook environment** (multi-user, isolated, browser-accessible) — the primary workspace
- **ML experiment tracking and model registry** — tracks runs, parameters, metrics, artifacts
- **SHAP/LIME** — pre-installed in the notebook base image, sample economic stress testing notebooks provided
- **Model serving** — trained model packaged as Docker image, deployed to the serving framework
- **Drift detection** — scheduled batch analysis comparing production input distributions to training distributions, alerts via the AIOps agent

**Is it JupyterHub?** Yes, but position it as a "multi-user notebook environment" — it's the right tool for economists who know Python. Don't overengineer this. A quant wants a Python environment with good libraries and GPU access when needed.

---

### "Prepare staff for supervision of payment technologies, like AI-based authentication" — what does this mean?

**This is a supervision use case, not a deployment use case.** CBO regulates the banks. The banks are deploying:
- Facial recognition / liveness detection for remote onboarding
- Behavioral biometrics (typing cadence, swipe patterns)
- AI-powered fraud detection on payment transactions
- Voice biometrics for call center authentication

CBO staff (regulators/supervisors) need to understand these technologies well enough to evaluate whether the banks deploying them are doing it responsibly. They want to **test and understand** these models — not deploy them to customers.

**What we provide:**
- **Sandboxed environment to run authentication models** from the open-source ecosystem (face detection, liveness detection, fraud classifiers) against synthetic/dummy data
- **Explainability tooling** so supervisors can understand HOW a biometric model makes its decision
- **Fairness assessment** to check if the authentication model has demographic bias (e.g., lower accuracy for darker skin tones — a known problem in facial recognition)
- **Pre-built sample notebooks** demonstrating these use cases with public biometric datasets

This is a strong use case that no commercial vendor will position better than us. A regulator who understands the technology makes better policy. The sandbox is a regulatory capability-building tool.

---

### A1 — What is the dataset for? RAG or ML training?

**Both. The requirement covers two different workflows that share the same storage layer.**

| Dataset Type | Who Uses It | How |
|-------------|-------------|-----|
| Documents (PDFs, Word, Arabic text) | All employees | Uploaded to document ingestion pipeline → chunked → embedded → stored in vector database → RAG queries |
| Structured data (CSVs, Excel, economic indicators) | Economists, data scientists | Uploaded to object storage → notebook environment reads it → ML model training / exploratory analysis |
| Synthetic transaction data | Innovation team, ML engineers | Generated or uploaded → used for fraud model training, payment analytics testing |

**The storage layer is the same (object storage).** The difference is what happens next:
- RAG path: document → ingestion pipeline → vector embedding → semantic search
- ML path: CSV/Excel → notebook environment → model training

Our architecture handles both naturally. One upload interface, two consumption patterns.

---

### A4/A5 — Could this be OCR, embedding, and enrichment of unstructured data?

**Yes. This is the most accurate reframe of these requirements.**

CBO has decades of regulatory documents, circulars, policy papers, meeting minutes, annual reports — most of them probably:
- Scanned PDFs (not machine-readable)
- Arabic + English mixed language
- Various formats (Word, PDF, Excel, legacy formats)
- Inconsistent structure across departments

Their "data preprocessing" requirement is not about SQL ETL pipelines. It is about: *"how do we take our messy document pile and make it AI-accessible?"*

**The correct solution:**

```
Raw documents (scanned PDF, Word, Excel, image)
  ↓
Document parsing (extract text from any format)
  ↓
OCR engine (for scanned documents — Arabic + English)
  ↓
Language detection + normalization
  ↓
Chunking strategy (semantic chunking, not fixed window)
  ↓
Embedding model (multilingual, Arabic-native)
  ↓
Vector database (with metadata: source, date, department, language)
  ↓
Graph database (entity relationships: which regulations reference each other)
  ↓
Semantic search ready
```

**What we need for this:**
- **Document parsing** — handles PDF, Word, Excel, HTML, images. Open source parsers exist (Apache Tika, Docling by IBM, Unstructured.io). Docling is particularly strong — it handles scanned PDFs with embedded OCR and understands table structure.
- **OCR for Arabic** — PaddleOCR (open source) has strong Arabic support. Or Tesseract with Arabic language pack.
- **Multilingual embedding** — already in our stack (BGE-M3, explicitly multilingual including Arabic)
- **Semantic chunking** — split documents at logical boundaries, not character count. Libraries exist.
- **Vector + graph store** — already in our stack (Milvus + Neo4j)

**Position A4/A5 as:** "AI-native document intelligence pipeline" — not "data cleaning tools." OCR + parsing + embedding + enrichment + semantic indexing. This is a genuinely impressive capability that directly serves CBO's use cases. Don't downplay it as ETL.

---

### A11 — The Workspace/Project Problem: Gitea + AI Coding Assistant + Browser Workspaces

This is a real problem that deserves a proper solution. Here's the full architecture:

**Core concept: A browser-accessible development workspace where every project is a Git repository, AI assistance runs on internal models, and all compute stays inside the sandbox.**

```
User opens browser → SSO (Identity Platform) → Workspace Portal
                                                        ↓
                               ┌────────────────────────────────────────┐
                               │         Project Dashboard               │
                               │  (backed by internal Git platform)     │
                               └────────────┬───────────────────────────┘
                                            │
                            ┌───────────────┼───────────────────┐
                            ↓               ↓                   ↓
                    Notebook Environment  Code Environment   Chat Interface
                    (for data scientists) (for developers)  (for all users)
                            │               │                   │
                    GPU compute access  AI coding assistant  RAG + agents
                    ML libraries        (internal AI model)  (internal models)
                    SHAP/LIME/Fairlearn  Git integration      No external calls
```

**Three workspace types:**

| Workspace Type | Who | What They Get |
|---------------|-----|--------------|
| **Notebook Workspace** | Economists, data scientists | Browser-based Jupyter notebook, Python, ML libraries, GPU access, SHAP/LIME pre-installed |
| **Code Workspace** | Developers, ML engineers | Browser-based VS Code (code-server), full IDE, AI coding assistant (Qwen Coder model via Continue.dev extension), Git integration, terminal |
| **Chat Workspace** | All staff | Conversational AI interface, document Q&A, agent presets for banking tasks |

**Project management layer:**
- Every project is a **Git repository** in the internal source control platform
- Project = repo = isolated workspace
- Teams collaborate via branches, PRs, wikis, and issues — exactly like GitHub but air-gapped
- Workspace pods are spawned with the project repo pre-cloned

**AI coding assistant:**
- The **Qwen 2.5 Coder** model (open source, strong coding capability) is deployed on the internal inference engine
- The **Continue.dev** VS Code extension connects the code workspace to this model
- Result: an internal GitHub Copilot equivalent — code completion, explanation, refactoring — powered entirely by the internal model, zero data leaves the sandbox
- Can also be integrated with Jupyter via the JupyterAI extension

**Browser-based remote access:**
- Light use case: VS Code Server (code-server) and JupyterHub both run natively in the browser — no additional gateway needed
- Heavy use case: if CBO needs full desktop environments (e.g., running GUI-based data tools), a browser-based remote desktop gateway (like Apache Guacamole) provides VNC/RDP access to desktop pods

**Git as the source of truth for projects:**
- Projects don't live in a proprietary project management system
- They live in Git — same GitOps principle we apply to infrastructure, applied to AI experiments
- Experiment reproducibility: if a notebook is in Git with its data references and requirements file, any team member can reproduce the result
- Model development has the same discipline as software development: branches, reviews, history, rollback

---

### Deployment Model: Two Options for CBO

**Option A: On-Premises GPU Infrastructure**
- CBO procures GPU servers (or rents co-location)
- We deploy and manage the entire platform on their hardware
- Full data sovereignty — no data ever leaves CBO's data center
- Higher upfront cost, maximum control
- Recommended for long-term production use and training workloads

**Option B: Huawei Cloud LLM as a Service (Huawei Partnership)**
- Platform control plane (orchestration, security, access management, observability) runs on lightweight cloud nodes — starting at 3 worker nodes
- LLM inference consumed as a managed API (Huawei Pangu or equivalent)
- Usage-based cost model: CBO pays per token consumed
- Starting cost: under 500 OMR/month for the platform infrastructure
- Scale model: same architecture, seamlessly scales to hundreds of nodes as usage grows — no re-architecture, just add nodes
- Lower entry cost, faster time to value

**Both options use the same platform architecture.** The only difference is where the GPU compute lives. The security, access control, observability, RAG pipeline, ML environment, and all other components are identical.

**Recommended approach for the proposal:** Offer both. Let CBO choose based on their budget cycle and procurement preferences. Highlight that Option B can start immediately and migrate to Option A when they're ready to commit to on-prem hardware.

---

### A15 — Tutorials & Knowledge Base: Why Is This Hard?

**It's not.** The user is right.

We generate the documentation, tutorials, and use case guides **using AI**, and we deliver them as part of the engagement. More importantly: we build the knowledge base **as a RAG application on the platform itself**.

This is a genuine differentiator: the first thing users do when they open the platform is ask the "AI Sandbox Guide" agent a question like *"How do I get started with economic stress testing?"* — and the guide answers them conversationally, with links to sample notebooks, step-by-step instructions, and relevant documentation.

The platform teaches you how to use the platform. This is AI-native onboarding.

**Content to produce (AI-generated, human-reviewed):**
- 10-15 sample use case notebooks (stress testing, document classification, Arabic regulatory Q&A, fraud pattern analysis, payment authentication testing)
- Quick-start guides for each workspace type
- API reference documentation
- Admin operations playbook

**Effort:** 2 weeks of AI-assisted content generation + review. Not a blocking concern.

---

### A16 — API Documentation: Just Serve It Through the Gateway

**Completely agree.** This is standard practice.

The inference engine already exposes an OpenAPI/Swagger spec at its documentation endpoint. The vector database, identity platform, and other services all have their own API specs.

Our API gateway (backed by an eBPF networking layer and an Envoy proxy) can aggregate all API specs and serve a unified Swagger UI at a single endpoint:

```
https://sandbox.cbo.gov.om/api-docs
```

Every service's API is documented there. Interactive, try-it-out capable, always up to date. Zero additional work — it's already there.

**Score for A16 should be revised to 85.** The gap is only CBO-specific operational documentation (which we create as content).

---

### A17 — Edge AI Inference: Cherry-Pick This Deliberately

Exactly right. Selectively acknowledging what's out of scope builds more credibility than claiming to cover everything.

**Proposed response language:** *"Edge AI inference — running optimized models on embedded or mobile hardware — is outside the scope of this sandbox implementation. The sandbox provides the environment for developing, training, and evaluating models. If CBO has specific edge deployment requirements (e.g., on-device fraud detection, branch network hardware), we welcome a separate scoping conversation with detailed business context."*

This signals: we are honest, we understand the boundary, and we are open to extending scope if justified.

---

### A19 — Multi-Project: Portal or Git?

**Both, combined.** Here's the thinking:

**Git is the project management foundation** — every experiment, model, and dataset reference lives in a repository. This gives you history, collaboration, branching, code review, and reproducibility for free.

**The portal is the front door** — not a separate project management system, but a lightweight dashboard that surfaces the Git projects, provides one-click workspace access, shows resource usage, and links to monitoring dashboards. Think of it as a catalog of running projects, not a system of record.

The catalog/portal concept (which we already have as part of Catalyst IDP) can be deployed as a lightweight web app:
- Shows all projects (backed by Git repos)
- Shows current workspace status (is your notebook pod running?)
- Links to monitoring dashboards
- Provides admin controls (resource quotas, access management)

**No need to build a complex portal from scratch.** A pre-configured developer portal (open source, lightweight) backed by our Git platform covers this requirement within 2 sprints.

---

### C3 — Model Version Control: The Full ML End-to-End Journey

Here is the complete journey for an ML practitioner in the CBO sandbox:

```
1. EXPLORE
   Open notebook workspace
   Connect to project repo (Git)
   Access training dataset from object storage
   Run exploratory data analysis

2. EXPERIMENT
   Train multiple model variants (different algorithms, hyperparameters)
   Each run automatically logged to ML experiment tracker:
     - Parameters: algorithm, learning rate, feature set
     - Metrics: accuracy, AUC, F1, RMSE
     - Artifacts: model file, feature importance plot

3. COMPARE & SELECT
   Review experiment dashboard
   Compare run metrics side by side
   Select best performing run

4. EXPLAIN & VALIDATE
   Run SHAP analysis: which features drove this prediction?
   Run LIME analysis: why did this specific instance get this score?
   Run fairness assessment: does the model treat demographic groups equally?
   Document findings in notebook (committed to Git)

5. REGISTER
   Promote selected model to model registry
   Model version tagged with:
     - Experiment run ID (lineage to training data + hyperparameters)
     - Validation metrics
     - Fairness report
     - Approval status

6. DEPLOY (in sandbox)
   Model packaged as container image
   Image signed with cryptographic key (supply chain security)
   Deployed via GitOps (config change in Git → automated deployment)
   Model serving framework exposes it as an API endpoint

7. MONITOR
   Performance metrics tracked over time (latency, throughput)
   Prediction distribution monitored for drift
   Input feature distributions compared to training baseline
   Specter AIOps agent alerts if drift threshold exceeded

8. RETRAIN (when needed)
   Drift alert triggers workflow
   New training data ingested
   Experiment cycle repeats from step 1
   New model version registered
   Old version archived (not deleted — full lineage preserved)
```

**What this requires in our stack:**
- Notebook environment (workspace, step 1-4)
- ML experiment tracker + model registry (steps 2-5)
- Container signing and image registry (step 6)
- GitOps delivery engine (step 6)
- Model serving framework (step 6)
- Observability platform + LLM/ML monitoring (step 7)
- Drift detection (step 7) — see below
- AIOps agent (step 7-8)

All of these exist in the OpenOva ecosystem. The ML experiment tracker and model registry (and the drift detection component) are additions to the standard Cortex stack but are standard open-source components.

---

### C4 — Bias Detection: More ML Evidence

Yes. This is another clear data point that CBO will train supervised ML models.

**Two distinct scenarios:**

**Scenario A: Classical ML model bias**
- CBO trains a credit risk model, fraud classifier, or economic indicator
- They need to check: does the model perform differently across borrower regions? Across bank size categories? Across sectors?
- Tool: fairness assessment library (Fairlearn, Aequitas) — runs in notebook environment
- This is genuine regulatory concern — a central bank that finds bias in a bank's AI model needs to understand how to detect it first

**Scenario B: LLM output bias**
- CBO uses an LLM for regulatory document analysis
- They want to check: does the LLM respond differently to questions about Islamic banking vs conventional banking? About different regions?
- Tool: LLM evaluation datasets + scoring (LangFuse evaluation with curated test cases)
- NeMo Guardrails can flag certain categories of biased outputs

Both are valid. Both are covered with our additions. The bias detection tooling lives in the notebook environment (pre-installed libraries) for Scenario A, and in the LLM observability platform for Scenario B.

---

### C6 — Model Drift Detection: What Is It and Can We Use an AIOps Agent?

**What is drift?**

Imagine CBO trains a stress testing model in 2022 using pre-pandemic economic indicators. In 2025, the model makes bad predictions because the world looks different — interest rate regimes changed, energy price dynamics changed, bank balance sheet compositions changed. The model didn't change, but the world did. That's drift.

Three types:

| Type | What Changes | Example |
|------|-------------|---------|
| **Data drift** | Input distributions shift | Economic indicator ranges in 2025 ≠ 2022 training data |
| **Concept drift** | The input→output relationship changes | Same GDP growth now predicts different NPL outcomes than before |
| **Performance drift** | Model accuracy degrades | F1 score on holdout set drops from 0.91 to 0.74 |

**How to detect it:**

- **Statistical tests** on input feature distributions: Kolmogorov-Smirnov test, Population Stability Index (PSI), chi-squared for categorical features
- **Model performance monitoring**: track accuracy/F1/AUC on a labeled holdout set over time
- **Prediction distribution monitoring**: if the model used to predict 3% NPL and now consistently predicts 8%, something shifted

**Open-source tool: Evidently AI** — generates drift reports as HTML dashboards or JSON metrics. Runs as a batch job or API. Integrates into any Python workflow.

**The AIOps agent angle — absolutely yes.** This is a perfect Specter use case:

```
Scheduled batch job: run Evidently drift analysis on model X
  ↓
Drift report generated (JSON metrics)
  ↓
Observability platform ingests metrics
  ↓
AIOps agent reads: PSI > 0.2 on feature "credit_growth_rate" ← drift threshold exceeded
  ↓
Agent creates alert: "Model CBO-STRESS-V2 showing significant data drift
  on input feature 'credit_growth_rate'.
  PSI = 0.31 (threshold: 0.2).
  Last training: 2024-03.
  Recommend retraining with data from 2024-03 to present."
  ↓
Notified to model owner via platform notification
  ↓
Owner reviews, approves retraining job
  ↓
Automated retraining pipeline executes (if approved)
```

This is AI managing AI. The AIOps agent monitors the health of ML models exactly the way it monitors the health of infrastructure components. Same pattern, different domain.

**Score for C6 should be revised to 55** with the AIOps agent + Evidently AI addition. Not a gap anymore — a differentiator.

---

### C12 — LIME and SHAP: More ML Evidence

Yes. Combined with C4, C6, and C3, this removes all ambiguity. CBO IS doing ML model development. The Security Policy was written by people who know what they're talking about.

**Position clearly:** "We provide a full ML development and explainability environment in the notebook workspace. SHAP and LIME are pre-installed. We provide 3-4 sample notebooks demonstrating explainability on banking use cases: credit risk explanation, fraud score explanation, and economic indicator model interpretation."

---

### C13/C14 — Ethical AI and Fairness: Are They Building an LLM?

**No, they are not building a general-purpose LLM.** They are building supervised ML models that make decisions with regulatory implications, and they need to ensure those decisions are explainable and fair.

A central bank that approves or scrutinizes a bank's AI-powered credit scoring system needs to understand:
- Can the model explain its decisions to a customer who was denied credit? (LIME/SHAP)
- Does the model treat all demographic groups equally? (Fairlearn)
- Are there feedback loops that could amplify existing inequalities? (Fairness assessment)

These are not LLM concerns. These are concerns about **consequential decision-making systems** in regulated industries. The CBO security team knows this landscape well.

**What we provide:**
- Fairness assessment library in the notebook environment — measures accuracy parity, demographic parity, equalized odds across protected groups
- Sample notebooks for banking fairness assessments
- Documentation: "How to conduct a fairness assessment before deploying a model in a regulated context"

This is also a regulatory capability that CBO can use to evaluate the models their supervised institutions are deploying. It doubles as a supervisory tool.

---

### D3 — Continuous Model Updates

Trivially solved with GitOps:

```
New model version available (e.g., Qwen3-32B released)
  ↓
Platform team evaluates and approves
  ↓
Update model configuration file in Git (one line change)
  ↓
GitOps delivery engine reconciles
  ↓
Model serving framework pulls new version
  ↓
Zero-downtime rollout (rolling update)
  ↓
Done
```

No manual intervention. No downtime. Full audit trail (Git commit history shows who approved what model version when). Rollback is a Git revert.

For open-source models: we can automate weekly checks for new model versions, generate a PR, require a human approval, then auto-deploy. This is standard MLOps practice.

**Score: 90.** This is solved.

---

### C11 — AI-Specific Incident Response: Position the AIOps Agent

The AIOps agent is the right answer here. Position it as follows:

**What AI-specific incidents look like:**
- A model starts generating harmful, biased, or confidential-data-leaking outputs
- A guardrail is being systematically bypassed via adversarial prompts
- A model serving endpoint is being abused (anomalous call volume, unusual query patterns)
- An ML model's predictions are drifting dangerously (credit risk model suddenly very optimistic)
- A fine-tuning job was submitted with production data instead of synthetic data

**What the AIOps agent provides:**
- Pre-built detection patterns for AI-specific anomalies (guardrail bypass frequency, LLM output anomaly scoring, model drift signals)
- Automated alert generation with context: not just "anomaly detected" but "Model X has had 47 guardrail bypass attempts in the last 10 minutes from user Y — possible adversarial probing"
- Suggested containment actions: "Recommend: rate-limit user Y, review their prompt history in LangFuse, notify security team"
- Incident ticket creation in the project management system
- Post-incident: evidence package from the LLM observability platform (full prompt/response audit trail)

**What we deliver as a document:** An AI Incident Response Playbook — 10-12 specific AI incident scenarios with detection signals, containment steps, and recovery procedures. This is a professional services deliverable, 1-week effort.

---

### WHY OPEN SOURCE IS AI-NATIVE (NOT JUST COST-EFFECTIVE)

This must be a prominent section in our proposal. It's our deepest differentiator.

**The core argument:**

For AI to manage an infrastructure platform, it needs to:
1. **Read** the platform's state (CRDs, metrics, logs, configs)
2. **Understand** what it's reading (semantic knowledge of each component)
3. **Reason** about problems (known failure modes, dependency graphs)
4. **Act** on it (remediation steps, configuration changes)

With **proprietary/closed-source platforms**, steps 2 and 3 are impossible:
- The components are black boxes
- CRDs don't exist — configs are vendor-specific and undocumented
- Failure modes are trade secrets
- Dependency graphs are internal to the vendor
- AI can at best read the logs and guess

With **open-source platforms**, all four steps are possible:
- Every component is publicly documented, including its failure modes
- CRDs are published, typed, and versioned — machine-parseable by design
- Integration dependencies are in the open-source community's documentation
- Our AIOps agent has been pre-trained on the semantic knowledge of all 52 components
- The AI doesn't need to guess — it knows

**The AIOps agent's semantic knowledge moat:**

If a component's disk fills up and it starts returning errors, the AIOps agent doesn't just see "HTTP 500 errors." It knows:
- This component uses MinIO for object storage (from the integration graph)
- MinIO fills up when model artifacts exceed the storage tier threshold (from failure mode knowledge)
- The remediation is to trigger MinIO's tiering to archival storage, or increase PVC size (from the remediation knowledge base)
- This specific error pattern precedes a full outage in 87% of observed cases (from the operational knowledge)

A closed-source platform cannot give an AI agent this depth. The AI has to work with what the vendor exposes, which is almost never enough.

**Token efficiency as an economic argument:**

Every LLM call costs money. When an AIOps agent needs to diagnose an issue on a closed-source platform, it dumps hundreds of lines of raw logs into a prompt and hopes the LLM figures it out. This is expensive, slow, and unreliable.

When the same agent works on an open-source platform with structured CRDs and unified telemetry, it sends a 200-token structured context block:
- Component type and version
- Relevant metric values at the time of alert
- Known failure modes for this component
- Recent changes (from Git history)

10x fewer tokens. 10x faster. 10x more accurate. This is an architectural moat, not a feature.

**Open source as regulatory advantage:**

For a central bank specifically: open source means the regulator can inspect, audit, and understand every line of code running in their infrastructure. There are no vendor black boxes. When the auditors ask "how does your credit risk model processing pipeline work?", CBO can show them the code. All of it. This is not possible with proprietary platforms.

---

## THINGS WE SHOULD BUILD OR ADD (BEYOND CURRENT CAPABILITIES)

These are net-new additions to the platform, all open source or buildable with AI-assisted coding:

| Addition | Purpose | Build or Adopt |
|----------|---------|---------------|
| Document intelligence pipeline | OCR + parsing + Arabic text processing + chunking | Adopt (Docling + PaddleOCR) |
| Multi-user notebook environment | Python workspaces, GPU access, ML libraries | Adopt (JupyterHub) |
| Visual AI pipeline builder | No-code/low-code pipeline construction | Adopt (Flowise) |
| Browser-based code environment | VS Code in browser + AI coding assistant | Adopt (code-server + Continue.dev) |
| ML experiment tracker + model registry | Experiment runs, model versioning, lineage | Adopt (MLflow) |
| PII detection and anonymization | Static dataset sanitization (Arabic-aware) | Adopt (Presidio) |
| Drift detection engine | Scheduled batch drift analysis for ML models | Adopt (Evidently AI) |
| Fairness assessment toolkit | Bias measurement across demographic groups | Adopt (Fairlearn) |
| Synthetic data generator | Generate statistically realistic fake banking data | Adopt (Gretel Synthetics or SDV) |
| Developer portal | Project catalog, workspace launcher, resource dashboard | Build lightweight (2 sprints, AI-coded) |
| AI Incident Response Playbook | Documentation artifact | Generate via AI + review |

---

## WHAT THE COMPLETE SOLUTION ARCHITECTURE LOOKS LIKE

**Four layers, concept names only:**

```
┌─────────────────────────────────────────────────────────────────────┐
│  LAYER 1: WORKSPACES (How users interact)                           │
│                                                                     │
│  Conversational Interface    Notebook Environment    Code Workspace │
│  ─────────────────────────   ──────────────────────  ─────────────  │
│  Chat, RAG, agent presets    Python, ML libraries,   VS Code in     │
│  Arabic + English            SHAP/LIME/Fairlearn,    browser, AI    │
│  Banking agent personas      GPU access on-demand    code assistant │
│                                                                     │
│  No-Code Pipeline Builder    Project Portal                         │
│  ─────────────────────────   ──────────────────                     │
│  Drag-and-drop AI flows       Git-backed projects,                  │
│  RAG builders, no Python      workspace launcher,                   │
│  needed                       resource dashboard                    │
└────────────────────────────────────┬────────────────────────────────┘
                                     │
┌────────────────────────────────────▼────────────────────────────────┐
│  LAYER 2: AI SERVICES                                               │
│                                                                     │
│  LLM Inference Engine    Document Intelligence    ML Platform       │
│  ──────────────────────  ───────────────────────  ─────────────     │
│  Pretrained open-source  OCR + parsing + Arabic   Experiment       │
│  models, OpenAI compat   text processing,         tracking,        │
│  API, GPU-accelerated    chunking pipeline        model registry,  │
│                                                   drift detection  │
│  Safety & Guardrails     Embedding + Vector Store  Graph Knowledge │
│  ──────────────────────  ──────────────────────── ──────────────── │
│  Prompt firewall, PII    Semantic search,         Entity graphs,   │
│  filter, topic control,  hybrid dense+sparse      regulatory       │
│  output validation       retrieval, Arabic-native  relationships   │
│                                                                     │
│  LLM Observability       Data Layer                                 │
│  ──────────────────────  ──────────────────────────────────────     │
│  Every AI call traced,   Encrypted object storage, PII             │
│  cost tracked, quality   anonymization pipeline, synthetic         │
│  scored, audit trail     data generation                           │
└────────────────────────────────────┬────────────────────────────────┘
                                     │
┌────────────────────────────────────▼────────────────────────────────┐
│  LAYER 3: PLATFORM FOUNDATION                                       │
│                                                                     │
│  Identity & Access Mgmt  Source Control + CI/CD   GitOps Engine    │
│  ──────────────────────  ──────────────────────── ──────────────── │
│  RBAC, MFA, JIT,         Git repos per project,   Declarative      │
│  SSO across all tools,   Gitea Actions pipelines, state mgmt,      │
│  FAPI 2.0                model deployment CI/CD   auto-reconcile   │
│                                                                     │
│  Security Stack          Full-Stack Observability  AIOps Agent      │
│  ──────────────────────  ─────────────────────── ──────────────    │
│  Runtime threat detect,  Metrics, logs, traces,  Pre-built AI      │
│  policy engine, WAF,     GPU dashboards,          knowledge of     │
│  supply chain signing,   LLM + ML monitoring,    every component, │
│  SIEM + alerting         tamper-evident logs      self-healing     │
└────────────────────────────────────┬────────────────────────────────┘
                                     │
┌────────────────────────────────────▼────────────────────────────────┐
│  LAYER 4: INFRASTRUCTURE                                            │
│                                                                     │
│  Option A: On-Premises GPU        Option B: Huawei Cloud LLM-as-a-S │
│  ──────────────────────────────   ──────────────────────────────── │
│  CBO-owned hardware, full air-gap  Platform infra on minimal nodes  │
│  IaC-provisioned, GitOps-managed   LLM inference via Huawei API    │
│  Maximum sovereignty              Starting <500 OMR/month          │
│  Scales without re-architecture   Same platform, different compute │
│                                                                     │
│  Both options: 100% IaC, Git-tracked, Flux-reconciled, Crossplane  │
│  managed. Every change is a commit. Every state is reproducible.   │
└─────────────────────────────────────────────────────────────────────┘
```

---

## REVISED HEAT MAP SCORES (After Ideation)

Several scores need updating based on this discussion:

| Req | Old Score | New Score | Reason |
|-----|-----------|-----------|--------|
| A4 | 35 | 65 | Reframed as document intelligence pipeline — we have a strong story |
| A5 | 10 | 60 | Same reframe — OCR + embedding is our capability |
| A15 | 5 | 70 | AI-generated content + RAG-powered guide reduces this to a 2-week deliverable |
| A16 | 68 | 85 | Swagger/OpenAPI via gateway is standard, already there |
| C6 | 32 | 60 | AIOps agent + Evidently AI makes this a differentiator, not a gap |
| D3 | 62 | 90 | GitOps model updates are trivial |

**Revised overall score: ~74/100 today → ~90/100 with additions**

---

## WHAT WE DECIDE TO BUILD vs WHAT WE ACKNOWLEDGE IS OUT OF SCOPE

**We BUILD/ADD:**
- Document intelligence pipeline (OCR + parsing + Arabic text processing)
- Multi-user notebook environment with ML libraries + SHAP/LIME/Fairlearn pre-installed
- Visual no-code AI pipeline builder
- Browser-based code environment with AI coding assistant (Qwen Coder)
- ML experiment tracking and model registry
- PII detection and anonymization microservice
- Drift detection engine with AIOps agent integration
- Lightweight developer portal (AI-coded, 2 sprints)
- AI Incident Response Playbook (document deliverable)
- Fairness assessment toolkit in notebook environment
- Sample use case notebooks (AI-generated, 10-15 banking-relevant)

**We ACKNOWLEDGE as out of scope (builds credibility):**
- Edge AI inference on embedded/mobile devices — honest, ask for business case
- Foundation LLM training from scratch — not relevant for a bank sandbox
- GPT (proprietary) self-hosting — impossible; we offer equivalent open-source models + API gateway option

---

*This is the ideation document — source for the proposal response*
*Next step: structure the RFP response using concept names, not product names*
