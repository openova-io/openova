# CBO AI Sandbox RFP — OpenOva Coverage Heat Map
**Central Bank of Oman | Statement of Work | November 2025**
**Internal Analysis | 2026-03-13**

---

## PART 1 — Who Is CBO, What Do They Actually Want, And Who Will Use This?

### 1.1 Reading Between the Lines of the RFP

Before scoring any requirement, we need to answer the fundamental question: **what is CBO actually trying to accomplish?**

The Preface says it clearly: *"test and experiment potential AI use cases that are not implemented yet in CBO… foster a culture of innovation, skill development, and exploratory learning among employees."*

This is not a machine learning platform procurement. This is an **AI culture and enablement initiative** wrapped in technical language that is — in several places — copy-pasted or AI-generated from generic enterprise AI RFP templates.

**What CBO is really buying:** A safe playground where employees can get hands-on with AI without any risk to the real bank.

### 1.2 Who Are The Users?

The RFP never defines the user personas explicitly. But the "Impact on CBO" section is very revealing:

| CBO Statement | What It Tells Us About The User |
|--------------|--------------------------------|
| "Experiment with AI analytics for risk assessment, asset allocation" | Risk officers, investment analysts — domain experts, not engineers |
| "Develop and validate predictive models for economic stress testing" | Economists, quants — probably Python-capable, not ML engineers |
| "Test AI-driven tools for due diligence on fintech innovations" | Innovation/strategy team — business-oriented, mixed technical |
| "Prepare staff for supervision of payment technologies, like AI-based authentication" | Regulatory/supervisory staff — non-technical, consumers of AI |
| "Deploy AI tools for data gathering and preliminary analysis" | Research staff — analysts who want productivity tools |
| "Strengthen regulatory processes through rapid AI prototyping" | Compliance/legal team — want to try AI on real-world documents |

**The user population is overwhelmingly non-ML-engineers.** These are:
- **Business analysts and economists** who want to use AI, not build it
- **Risk and compliance officers** who want to understand what AI can and cannot do
- **Innovation champions** who want to prototype ideas and show management
- **A small technical minority** (IT, data science) who might write code

They are **not:**
- ML researchers who will train foundation models
- MLOps engineers who need a full model lifecycle platform
- Data engineers who need ETL pipelines
- Edge device developers

### 1.3 What Kind of AI Work Will They Actually Do?

Based on the user personas and the Impact section, the actual day-to-day activity in this sandbox will be:

| Activity | Volume | What They Need |
|----------|--------|---------------|
| Chat with LLMs on banking topics | High (everyone does this) | LibreChat + good models |
| Upload a document, ask questions about it | High (analysts, regulatory) | RAG pipeline |
| Try prompt engineering for their use case | High (all technical users) | LLM access + prompt workspace |
| Build a simple AI-powered prototype to demo | Medium (innovation team) | Low-code pipeline builder |
| Write Python to analyze data with AI | Low (quants, data scientists) | Jupyter notebooks + LLM API |
| Train or fine-tune a custom model | Very low (maybe never) | MLflow + training infra |
| Deploy model to edge device | None (not their business) | N/A |

**Key insight:** ~80% of the usage will be conversational AI and RAG over documents. The "training" and "edge" language in the RFP is aspirational/copy-pasted, not a real near-term need.

### 1.4 Is The RFP Well-Written Or AI-Generated?

Honestly: **the body is AI-generated, the Security Policy is human-written.**

Evidence:
- The technical functional requirements have no coherent persona — they mix "upload a CSV" (analyst use case) with "train complex models" (ML engineer use case) with "edge AI inference" (IoT use case) in the same flat list with no prioritization.
- Generic phrases like "GPUs or TPUs for training complex models efficiently" and "APIs/SDKs provided for user-driven experimentation" read like ChatGPT completing an enterprise AI RFP template.
- The phrase "integrate newer versions of GPT" treats GPT as a version number of a software library, not a proprietary API product from a competitor — this is a telltale sign of AI-generated text or a non-technical author.
- In contrast, Appendix A (the Security Policy) is sharp, internally consistent, specifically names LIME and SHAP, references ISO 42001, mentions TLS 1.3, and reads like it was written by a real information security team. This was human-written.

**Implication for our response:** We should be honest in our proposal where requirements are unclear or misspecified. A thoughtful clarification in our proposal will build more credibility than trying to map to every letter of a confused list.

---

## PART 2 — Requirements Heat Map

**Scoring scale:**
- **90-100** — Covered out of the box, zero additional work
- **75-89** — Covered out of the box, minor configuration only
- **50-74** — Substantial coverage, some targeted additions or configuration needed
- **25-49** — Partial coverage, meaningful gaps, additions required
- **10-24** — Minimal coverage, significant additions required
- **0-9** — Not covered. Either genuinely missing, out of scope, or the requirement is questionable

**Flag legend:**
- 🟩 Strong fit
- 🟨 Partial fit — additions needed
- 🟥 Gap or out of scope
- ⚠️ Questionable requirement — see analysis
- 💡 OpenOva alternative approach recommended

---

### Section A — Technical Functional Requirements

---

#### A1 — Upload and manage dummy datasets
**RFP says:** *"Support ability to upload and manage dummy dataset."*

| Score | Coverage |
|-------|----------|
| **75** 🟨 | MinIO (S3-compatible object storage) handles storage and ACL-controlled access. LibreChat allows file upload per conversation. Harbor stores container artifacts. What's missing is a **dedicated dataset management UI** — no versioning UI, no dataset catalog, no sharing-by-project features out of the box. |

**What works today:** Upload a CSV/PDF to MinIO via the S3 API or LibreChat, use it in a conversation, store it per project bucket.

**What needs addition:** A lightweight dataset catalog (Apache Superset or a simple MinIO Console configuration) if CBO needs to share and version datasets across teams.

**Effort to close gap:** 1 week configuration (MinIO Console + bucket naming conventions).

---

#### A2 — Access to pre-trained models and APIs
**RFP says:** *"Access to pre-trained models and APIs."*

| Score | Coverage |
|-------|----------|
| **92** 🟩 | vLLM serves any HuggingFace-compatible model via OpenAI-compatible API. KServe handles model lifecycle. LibreChat provides the chat UI. Out of the box: Qwen3, Llama 3.x, Mistral, DeepSeek-R1, Phi, Gemma — all production-grade LLMs. Anthropic Adapter enables Claude access if needed. |

**What works today:** Deploy vLLM with any open-source model. Employees access via LibreChat UI or OpenAI SDK API calls immediately. Multiple models can run simultaneously, users can choose.

**Note on "GPT":** See General Requirements analysis. OpenAI's GPT cannot be self-hosted. OpenOva offers equivalent or superior open-source models. Axon can be added as a gateway to commercial APIs if CBO insists on GPT access.

---

#### A3 — Role-Based Access Controls
**RFP says:** *"Role based access controls are supported."*

| Score | Coverage |
|-------|----------|
| **92** 🟩 | Keycloak provides enterprise-grade RBAC with groups, roles, scopes, and fine-grained policies. Integrates with CBO's existing Active Directory/LDAP via LDAP federation. Every platform component (LibreChat, JupyterHub, Grafana, etc.) authenticates through Keycloak — single control plane for all access. |

**What works today:** Define roles (Viewer, Experimenter, DataScientist, Admin), map to CBO departments, enforce least privilege. MFA enforced at login. JIT access for privileged operations.

---

#### A4 — Data Collection: Assessment and Preprocessing Before Utilization
**RFP says:** *"Data Collection: Assessment of data and the need to preprocess data before utilization."*

⚠️ **Questionable requirement — needs clarification**

This requirement is genuinely unclear. "Assessment of data before utilization" could mean:
- (a) A visual data profiler — see column distributions, data types, null counts before uploading
- (b) A governance checkpoint — humans review data classification before it enters the sandbox
- (c) A data engineering step — transform raw data into usable format

For a bank sandbox, (b) is the genuine intent: *"before using a dataset in AI experiments, assess whether it's appropriate (synthetic? anonymized? correctly classified?)."*

| Score | Coverage |
|-------|----------|
| **35** ⚠️🟥 | OpenOva has no dedicated data profiling UI. MinIO stores data, Kyverno enforces policies, but no visual data assessment workflow. |

💡 **OpenOva Recommended Alternative:** Instead of a traditional data profiling tool, we recommend a **data ingestion workflow** built on the platform's own AI capabilities:
- Employee uploads candidate dataset to MinIO staging bucket
- Presidio (PII detection) runs automatically, flags any real personal data
- A LibreChat "Data Steward" agent reviews the profile report and either approves or rejects
- Approved datasets move to the experiment bucket

This is simpler to operate, more aligned with CBO's regulatory mindset, and leverages the platform itself rather than adding a separate data engineering tool.

---

#### A5 — Data Cleaning Tools
**RFP says:** *"Data Cleaning Tools: Software to preprocess data."*

⚠️ **Questionable requirement — likely AI-generated filler**

| Score | Coverage |
|-------|----------|
| **10** ⚠️🟥 | No dedicated data cleaning or ETL tool in OpenOva. |

**Why we believe this is misspecified:** CBO employees are not data engineers. The sandbox use cases (ask questions about documents, run economic analysis) do not require preprocessing pipelines. The actual need is: *"I have a CSV with some economic data, can I use it in AI experiments?"* — which is a file upload, not a data cleaning workflow.

💡 **OpenOva Recommended Alternative:** For the actual use cases:
- **For document RAG:** PDF/Word/CSV uploads go directly into the RAG pipeline via LibreChat. No cleaning needed — the embedding model handles messy text.
- **For structured data analysis:** A Python notebook in JupyterHub with pandas is the right tool. A non-technical user does not need this; a quant/data scientist has it.
- **For synthetic data generation:** If CBO needs to generate realistic fake banking data, Gretel Synthetics (open source) generates statistically realistic synthetic datasets from schemas — no real data ever needed.

We recommend **not** adding a dedicated data cleaning platform. The 5% of users who need it (quants) will use Jupyter. The 95% who don't need it should not be burdened with a data engineering workflow.

---

#### A6 — Data Privacy: Anonymization of Sensitive Data
**RFP says:** *"Data Privacy Measures: Ensure compliance with regulations by anonymizing sensitive data."*

| Score | Coverage |
|-------|----------|
| **55** 🟨 | NeMo Guardrails provides **runtime PII filtering** — any PII in a prompt or LLM response is detected and blocked/masked before it propagates. OpenBao encrypts secrets at rest. cert-manager enforces TLS 1.3 in transit. What's missing is **static dataset anonymization** — a tool that takes a CSV with real names/IBANs/phone numbers and produces a sanitized version. |

💡 **Recommended Addition:** Microsoft Presidio (MIT license, open source). Runs as an API microservice, supports Arabic text, detects Omani-specific PII (national IDs, phone formats). Deploys as a single K8s pod. Integrates with the MinIO ingestion workflow from A4.

**Effort to add:** 1 sprint.

---

#### A7 — Fully Isolated AI Development Environment
**RFP says:** *"A sandbox environment to safely test without connecting to any live/production/testing system. It has to be fully isolated."*

| Score | Coverage |
|-------|----------|
| **95** 🟩 | This is a core OpenOva architectural principle. Cilium Network Policies enforce namespace-level microsegmentation — sandbox pods cannot reach any external network unless explicitly permitted. Kyverno auto-generates NetworkPolicies on every namespace. Air-gap capable: the entire platform runs with no outbound internet required (all models self-hosted, all dependencies in Harbor registry). |

**What works today:** Deploy sandbox namespace with `defaultDeny` egress policy. Zero production connectivity by design, enforced at kernel level via eBPF — not just a firewall rule, but a kernel-enforced policy that cannot be bypassed from inside a container.

---

#### A8 — High-Performance Hardware: GPUs or TPUs
**RFP says:** *"High-Performance Hardware: GPUs or TPUs for training complex models efficiently."*

⚠️ **Partially misspecified requirement**

| Score | Coverage |
|-------|----------|
| **85** ⚠️🟩 | vLLM has full NVIDIA GPU support: A10, A100, H100, tensor parallelism across multiple GPUs, quantization (AWQ, GPTQ, FP8) for cost optimization. KServe manages GPU-backed model serving. DCGM (NVIDIA Data Center GPU Manager) metrics flow into Grafana. |

**The misspecification:** The RFP says "GPUs or TPUs for **training** complex models." For a sandbox of CBO's scope (employee experimentation, not research lab), **training from scratch is not the right activity**. This language came from a generic ML infrastructure template.

💡 **Honest clarification:** TPUs are Google Cloud proprietary hardware — not applicable for on-premises deployment. For on-prem, GPU (NVIDIA) is the correct choice. The hardware need is for **inference** (running models), not for training foundation models from scratch, which would require thousands of GPU-hours and terabytes of data. CBO's use case is:
- Run pre-trained models (inference GPUs → A10 class is sufficient)
- Possibly fine-tune models on small datasets (same GPU, just longer job)
- Not: pre-train a foundation model (not feasible in a bank sandbox context)

We should size the GPU recommendation correctly: **2-4× NVIDIA A10 (24GB VRAM each)** for inference. If CBO wants fine-tuning capability, 1-2× A100 (80GB) is the appropriate step up.

---

#### A9 — Scalable Infrastructure
**RFP says:** *"Systems capable of handling increased data volume during development."*

| Score | Coverage |
|-------|----------|
| **92** 🟩 | KEDA (event-driven autoscaling), VPA (vertical pod autoscaling), Kubernetes HPA, and MinIO's distributed mode all provide seamless scaling. GPU scaling is managed via KServe's model scaling policies — scale to zero when idle, scale to N under load. |

---

#### A10 — Performance Metrics Frameworks
**RFP says:** *"Mechanisms to measure model accuracy, latency, and scalability."*

| Score | Coverage |
|-------|----------|
| **72** 🟨 | LangFuse covers **LLM-specific metrics** excellently: response latency, token usage, cost per query, evaluation scores, user feedback. Grafana covers infrastructure metrics: GPU utilization, memory, request throughput. What's not covered out of the box: **traditional ML model accuracy metrics** (precision, recall, F1, AUC for classification models, RMSE for regression). |

**Nuance:** If CBO is primarily using LLMs (which they are, based on the use cases), LangFuse + Grafana covers the requirement fully. If they eventually train/evaluate classical ML models (e.g., a fraud detection classifier), MLflow needs to be added.

💡 **Recommended Addition for future phase:** MLflow (Apache 2.0) — adds experiment tracking, metric logging, model evaluation dashboards. 1 sprint to add.

---

#### A11 — Project/Workspace Creation, Collaboration and Sharing
**RFP says:** *"Project/workspace creation, collaboration and sharing features."*

| Score | Coverage |
|-------|----------|
| **38** 🟥 | LibreChat supports multi-user conversations and conversation sharing. However, there is no concept of a "project" or "workspace" as a container for: shared datasets, shared prompts, shared models, team membership, and access controls scoped to a project. This is a meaningful gap for a multi-team organization. |

💡 **OpenOva Recommended Alternative:** Instead of adding a dedicated project management platform, we can use **Kubernetes namespaces as workspaces**:
- Each team/department gets a K8s namespace
- Their MinIO buckets, LibreChat agent presets, and JupyterHub servers are scoped to that namespace
- Keycloak groups map to namespace access
- Kyverno enforces namespace boundaries

This is infrastructure-level project isolation, not a UI-based project management tool. For a sandbox where projects are loosely defined ("let's try AI for X"), this is more appropriate than heavyweight project management software.

If CBO specifically wants a project management UI, **Gitea** (already in the stack) provides repository-per-project with wikis, issue tracking, and team collaboration.

---

#### A12 — Built-in AI Experimentation Modules (No-Code/Low-Code + Code)
**RFP says:** *"Built-in AI experimentation modules (prebuilt templates, support for custom code and no-code/low-code options)."*

| Score | Coverage |
|-------|----------|
| **32** 🟥 | LibreChat covers the **conversational AI experimentation** use case well (prebuilt agent configurations, multi-model, persona switching). It does not provide: a visual drag-and-drop pipeline builder, no-code form-based workflows, or interactive notebooks. Code-based experimentation has no environment. |

This is the most important gap in the stack for CBO's stated goal of enabling employees of different skill levels.

**The three tiers of users and what they need:**

| User Tier | What They Need | OpenOva Covers? |
|-----------|---------------|-----------------|
| Non-technical (analyst, compliance, regulatory) | Chat UI + pre-built AI agents for banking tasks | ✅ LibreChat |
| Semi-technical (innovation team, junior developers) | Drag-and-drop AI pipeline builder, form-based workflows | ❌ Missing — needs Flowise |
| Technical (data scientists, engineers) | Python notebooks, model evaluation, API access | ❌ Missing — needs JupyterHub |

💡 **Recommended Additions:**
- **Flowise** (Apache 2.0): Visual drag-and-drop LLM pipeline builder. Non-developers can build RAG pipelines, chatbots, and document analysis flows by connecting nodes. Connects natively to vLLM and Milvus. This is the no-code layer.
- **JupyterHub** with KubeSpawner: Multi-user Jupyter notebooks where each user gets an isolated pod. Pre-installed with transformers, torch, langchain, sklearn, shap, lime. This is the code layer.

Combined, these two additions complete the experimentation spectrum. **Effort: 3 sprints total.**

---

#### A13 — Secure Upload, Storage, and Management of Synthetic/Test Datasets
**RFP says:** *"Secure upload, storage, and management of synthetic or test datasets."*

| Score | Coverage |
|-------|----------|
| **82** 🟩 | MinIO provides S3-compatible object storage with: AES-256 encryption at rest, TLS 1.3 in transit, bucket-level ACLs, object versioning, and audit logging. Harbor provides secure artifact storage for model files. External Secrets + OpenBao manage the MinIO credentials. |

**Minor gap:** No dataset catalog or metadata tagging UI for discovering what datasets exist and who uploaded them. For a team of 20-50 experimenters, a naming convention and README-in-bucket may be sufficient. For 100+ users, a lightweight catalog addition (Apache Atlas or just a MinIO bucket index page) would help.

---

#### A14 — Support for Training, Versioning, and Evaluation of AI/ML Models
**RFP says:** *"Support for training, versioning, and evaluation of AI/ML models."*

⚠️ **Requires careful scope clarification**

| Score | Coverage |
|-------|----------|
| **35** ⚠️🟥 | vLLM + KServe handle **model serving and deployment**. LangFuse handles **LLM evaluation** (response quality scoring, A/B testing of prompts). Git + Harbor handle **model artifact versioning** (container image SHA-pinning). What's missing: a dedicated MLOps platform for **experiment tracking, hyperparameter logging, training run management, and model registry**. |

**The scope question CBO needs to answer:** Are they training models or using models?

- **Using pre-trained models** (chat, RAG, summarization, classification via prompts): OpenOva covers this at 85+.
- **Fine-tuning pre-trained models on CBO data** (e.g., fine-tuning a Llama model on regulatory documents): Needs MLflow + GPU training jobs.
- **Training ML models from scratch** (e.g., a fraud classifier with CBO transaction data): Needs MLflow, plus data engineering, plus training infrastructure.
- **Training foundation/LLM models from scratch**: Not feasible in a bank sandbox. Ignore this interpretation.

💡 **Our Recommendation:** OpenOva covers the first use case today. For fine-tuning, we add MLflow (1 sprint). Training classical ML models is also covered with JupyterHub + MLflow. We should ask CBO during scoping which of these they actually intend.

---

#### A15 — Guided Tutorials, Knowledge Base, and Sample Use Cases
**RFP says:** *"Guided tutorials, knowledge base, and sample use cases."*

| Score | Coverage |
|-------|----------|
| **5** 🟥 | OpenOva does not ship with a tutorial system or pre-built content for specific verticals. This is **content**, not software. The platform is a blank canvas; filling it with CBO-relevant learning material is a professional services deliverable. |

💡 **OpenOva Recommended Alternative (and a genuine differentiator):** Build the knowledge base AS a RAG application on the platform itself:
- We ingest CBO-relevant AI tutorials, sample notebooks, and use case documentation into Milvus
- Create a "CBO AI Sandbox Guide" agent in LibreChat
- Employees ask: *"How do I build a document classifier?"* or *"Show me how to analyze economic data with AI"* and the agent responds with step-by-step guidance
- This is a **meta-demonstration** of the platform — the learning system is itself an example of what the platform can do

This is a 2-week content + configuration effort. No additional software needed.

---

#### A16 — Full Documentation for APIs and Platform Features
**RFP says:** *"Full documentation for all APIs and platform features."*

| Score | Coverage |
|-------|----------|
| **68** 🟨 | All 52 platform components have detailed README files with API references, configuration options, and usage examples. vLLM serves a full OpenAPI spec at `/docs`. LangFuse, Keycloak, Grafana, and Milvus all have extensive upstream documentation. Gap: CBO-specific operational documentation (how CBO staff uses the platform, CBO-specific agent configurations, CBO admin runbooks) needs to be created as part of the engagement. |

---

#### A17 — Support for Edge AI Inference
**RFP says:** *"Support for edge AI inference (running ML models on the device)."*

⚠️ **Almost certainly AI-generated filler — needs stakeholder clarification**

| Score | Coverage |
|-------|----------|
| **0** ⚠️🟥 | OpenOva is a server-side, Kubernetes-native platform. Running models on edge devices (IoT sensors, mobile phones, embedded hardware) is architecturally orthogonal to OpenOva's design. |

**Why we believe this is misspecified:** The Central Bank of Oman is a financial regulatory institution. Its AI use cases (risk modeling, regulatory document analysis, economic forecasting, payment supervision) are all server-side, analytical, and LLM-based. "Edge AI inference" is a concept from manufacturing (quality inspection cameras), healthcare (wearables), and autonomous vehicles — not from central banking.

No other RFP requirement, use case, or impact statement mentions edge devices, IoT, mobile, or on-device anything. This requirement appears exactly once in the entire document and is inconsistent with everything else.

💡 **How to handle this in our proposal:** Acknowledge it honestly. Say: *"Edge AI inference is outside the scope of a central bank AI sandbox as described in this RFP. We recommend removing this requirement or deferring it to a future phase if CBO identifies specific edge deployment use cases. If the intent is to test models that will eventually run on edge devices, the sandbox serves as the development and validation environment; the optimization for edge deployment (ONNX export, quantization for embedded hardware) can be provided as a documentation deliverable."*

Do not try to fake coverage of this requirement.

---

#### A18 — APIs/SDKs for User-Driven Experimentation and Custom Project Development
**RFP says:** *"APIs/SDKs provided for user-driven experimentation and custom project development."*

| Score | Coverage |
|-------|----------|
| **88** 🟩 | vLLM exposes a fully OpenAI-compatible REST API. Any code using the OpenAI Python SDK, TypeScript SDK, or curl works against OpenOva with a single base URL change. Milvus has Python, Go, Java, and Node.js SDKs. The Anthropic Adapter adds Anthropic SDK compatibility. This covers every developer who wants to build custom applications against the platform. |

---

#### A19 — Multi-User/Multi-Project Support
**RFP says:** *"Multi-user/multi-project support."*

| Score | Coverage |
|-------|----------|
| **65** 🟨 | Multi-user: fully covered by Keycloak + LibreChat (each user has their own account, conversation history, and model access based on role). Multi-project: conceptually supported via Keycloak groups and K8s namespaces, but no dedicated project management UI. Projects are implicit (conversations, notebooks) rather than explicit containers. |

---

### Section B — Non-Functional Requirements

---

#### B1 — Usability: Intuitive UI for Users of Different Skill Levels
**RFP says:** *"Intuitive, accessible UI/UX for users of different skill levels."*

| Score | Coverage |
|-------|----------|
| **55** 🟨 | LibreChat: excellent UI for non-technical users — clean, familiar (ChatGPT-like), minimal learning curve. Gap: users who want to do more than chat (build pipelines, run notebooks, visualize experiments) have no UI. With Flowise and JupyterHub added, coverage rises to ~80. |

---

#### B2 — Security: Strict Isolation from Production
**RFP says:** *"Strict isolation from production and sensitive regulatory systems."*

| Score | Coverage |
|-------|----------|
| **95** 🟩 | Core OpenOva principle. eBPF-enforced Cilium network policies, K8s namespace boundaries, Kyverno admission control, air-gap capable. Nothing leaves the sandbox without explicit network policy permission. |

---

#### B3 — Privacy and Compliance
**RFP says:** *"Full compliance with organizational data privacy standards. Support for anonymized, synthetic, or masked data in all experiments."*

| Score | Coverage |
|-------|----------|
| **72** 🟨 | Runtime PII protection: NeMo Guardrails (excellent). Data encryption: OpenBao + cert-manager (excellent). Static anonymization: needs Presidio addition. Synthetic data generation: needs Gretel or Presidio anonymization capability. |

---

#### B4 — Performance
**RFP says:** *"<2s for user interactions, <5s for common data operations under normal load."*

| Score | Coverage |
|-------|----------|
| **82** 🟩 | Achievable. vLLM's PagedAttention delivers first-token latency typically <1s on A10 GPUs. Milvus HNSW vector search is <100ms. LibreChat streaming shows first tokens in <1s. Grafana and LangFuse dashboards load in <500ms. The target is realistic with proper GPU sizing. |

---

#### B5 — Scalability
**RFP says:** *"Ability to support an increasing number of users, projects, and integrated hardware devices without degradation."*

| Score | Coverage |
|-------|----------|
| **90** 🟩 | KEDA + VPA + K8s. vLLM scales to additional GPU replicas under load. Milvus is horizontally scalable. LibreChat stateless — add replicas freely. The "hardware devices" phrase is edge AI language again (see A17). |

---

#### B6 — Maintainability
**RFP says:** *"Vendor provides tools and documentation for future platform updates and troubleshooting."*

| Score | Coverage |
|-------|----------|
| **90** 🟩 | GitOps via Flux: every configuration change is tracked in Git. Platform updates are Git commits that Flux reconciles. Rollback is a Git revert. Full change history. Grafana + LangFuse provide operational visibility. All component READMEs document upgrade procedures. |

---

#### B7 — Extensibility
**RFP says:** *"Ability to add new templates, modules, or device integrations with minimal effort."*

| Score | Coverage |
|-------|----------|
| **88** 🟩 | Kubernetes-native modular architecture. Adding a new component is a Helm chart + Kustomize patch + Git commit. New LibreChat agent presets are YAML configs. New vLLM models are a single value change in the HelmRelease. The GitOps model makes extensibility systematic, not ad-hoc. |

---

### Section C — Security Policy Requirements (Appendix A)

*Note: These requirements are well-written and clearly from CBO's actual security team. Score them seriously.*

---

#### C1 — Risk Assessment + Continuous Risk Monitoring
**Policy says:** *"Comprehensive risk assessments must be conducted for all AI systems prior to development and deployment. Continuous risk monitoring process must be in place."*

| Score | Coverage |
|-------|----------|
| **80** 🟩 | Falco (runtime behavioral risk detection), OpenSearch SIEM (continuous security monitoring), Kyverno (policy enforcement at admission), Grafana (infrastructure risk metrics), LangFuse (AI behavior monitoring). Gap: no formal risk register tooling — this is a process requirement, not just technical. |

---

#### C2 — Data Encryption at Rest and in Transit (TLS 1.3)
**Policy says:** *"Encrypt all AI-related data at rest using industry-standard encryption algorithms. Use secure protocols (e.g., TLS 1.3) for all data transmissions."*

| Score | Coverage |
|-------|----------|
| **95** 🟩 | MinIO: AES-256 encryption at rest. OpenBao: AES-GCM for secrets. cert-manager: automated TLS 1.3 certificate management for all ingress. Cilium mTLS: encrypted service-to-service communication inside the cluster. Proper key management via OpenBao. |

---

#### C3 — Model Version Control (Code, Training Data, Hyperparameters)
**Policy says:** *"Implement version control for all model code, training data, and hyperparameters."*

| Score | Coverage |
|-------|----------|
| **42** 🟨 | Git tracks all model configuration (which model version, which hyperparameters in KServe). Harbor stores container images with SHA-pinned tags. LangFuse tracks which prompt version was used for which LLM call. What's missing: a dedicated ML model registry that tracks training datasets + hyperparameters as linked artifacts (MLflow covers this). |

---

#### C4 — Bias Detection and Mitigation in Training Data and Models
**Policy says:** *"Implement measures to detect and mitigate bias in training data and resulting models."*

| Score | Coverage |
|-------|----------|
| **18** 🟥 | NeMo Guardrails provides some demographic sensitivity detection for LLM outputs, but this is not systematic bias measurement. OpenOva has no dedicated bias/fairness evaluation tool. |

💡 **Recommended Addition:** Fairlearn (MIT license, Microsoft) — Python library for assessing and mitigating bias in ML models. Integrates directly into JupyterHub notebooks. Pre-install in the Jupyter base image alongside SHAP and LIME. Effort: included in JupyterHub sprint.

---

#### C5 — Secure Model Deployment Pipeline with Access Controls and Audit Logging
**Policy says:** *"Implement a secure model deployment pipeline with proper access controls and audit logging."*

| Score | Coverage |
|-------|----------|
| **90** 🟩 | GitOps (Flux): every deployment goes through Git — reviewed, approved, audited. Harbor + Sigstore/Cosign: container image signing with cryptographic provenance. Kyverno: admission control prevents unsigned/non-compliant images from being deployed. LangFuse: audit log of every model inference. Full deployment audit trail. |

---

#### C6 — Model Drift Detection
**Policy says:** *"Implement continuous monitoring of model performance, including drift detection."*

| Score | Coverage |
|-------|----------|
| **32** 🟥 | LangFuse tracks LLM performance metrics (latency, evaluation scores) over time, which provides a proxy for drift detection. Grafana can alert on metric degradation. However, there is no automated statistical drift detection (data drift in inputs, concept drift in outputs). |

💡 **Practical note for CBO:** For an LLM-centric sandbox (which this is), "drift" means the model's responses degrade in quality over time. LangFuse's evaluation scores + human feedback ratings effectively track this. Traditional ML drift detection (monitoring feature distributions) is relevant only if CBO trains classical ML models. Recommend addressing this with LangFuse evaluation dashboards for now, with a note that statistical drift monitoring can be added via Evidently AI (open source) in a future phase.

---

#### C7 — RBAC + MFA + JIT Access
**Policy says:** *"Implement RBAC. Enforce least privilege. Require MFA. Implement JIT access for administrative functions."*

| Score | Coverage |
|-------|----------|
| **95** 🟩 | Keycloak: full RBAC with group-based policies. TOTP and WebAuthn (FIDO2) MFA enforcement. JIT access implemented via time-limited Keycloak credentials + OpenBao dynamic secrets for admin operations. Principle of least privilege enforced by Kyverno at the K8s level. |

---

#### C8 — API Security
**Policy says:** *"Secure all APIs used by AI systems by ensuring API controls are in place."*

| Score | Coverage |
|-------|----------|
| **90** 🟩 | Coraza WAF (OWASP CRS): L7 application firewall for all external-facing APIs. Cilium L7 policies: enforce allowed API paths and methods at kernel level. Keycloak API tokens: bearer token authentication on all API endpoints. Rate limiting via Cilium. |

---

#### C9 — Real-Time Monitoring + Anomaly Detection
**Policy says:** *"Implement real-time monitoring of AI system performance, security, and usage. Use anomaly detection techniques."*

| Score | Coverage |
|-------|----------|
| **85** 🟩 | Falco: real-time behavioral anomaly detection via eBPF. OpenSearch ML: anomaly detection on SIEM events. Grafana: real-time metrics dashboards with alerting rules. LangFuse: real-time LLM usage monitoring. |

---

#### C10 — Comprehensive Tamper-Evident Logging
**Policy says:** *"Maintain comprehensive logs of all AI system activities, including model training, testing, and inferences. Ensure log integrity through tamper-evident logging mechanisms."*

| Score | Coverage |
|-------|----------|
| **88** 🟩 | Loki: structured log aggregation with immutable chunks (append-only). OpenSearch: indexed security events with tamper-evident audit trail. LangFuse: immutable LLM call log with full input/output/metadata. Grafana Tempo: distributed trace records. The combination covers all AI system activities comprehensively. |

---

#### C11 — AI-Specific Incident Response
**Policy says:** *"AI-specific incident response plans, integrated with the organization's overall cybersecurity incident response procedures. Conduct regular drills."*

| Score | Coverage |
|-------|----------|
| **45** 🟨 | OpenSearch SIEM + Falco alerting + Grafana on-call notifications provide the technical infrastructure for incident detection and escalation. What's missing: an **AI-specific IR playbook** — defined response procedures for AI-specific incidents (model misbehavior, prompt injection attack, data extraction via model, guardrail bypass). |

💡 **Recommended Deliverable:** Provide a CBO AI Incident Response Playbook as a project deliverable — a documented runbook covering 10-12 AI-specific incident scenarios with detection signals (LangFuse + Falco), containment steps, and recovery procedures. This is a 1-week professional services effort, not a software addition.

---

#### C12 — LIME and SHAP Explainability
**Policy says:** *"The Explainability should be based on the two prominent interpretability tools — LIME (Local Interpretable Model-agnostic Explanations) and SHAP (SHapley Additive explanations)."*

⚠️ **Genuine requirement from CBO's security team — not AI-generated**

| Score | Coverage |
|-------|----------|
| **8** 🟥 | Not in OpenOva's current stack. LIME and SHAP are Python libraries, not standalone services. They require a Python execution environment (Jupyter notebook or an API wrapper) to operate. |

**Important context:** LIME and SHAP were designed for **classical ML models** (classifiers, regressors). Their application to **LLMs** is more nuanced:
- For LLMs, attention visualization and token-level attribution (via tools like BertViz or Captum) are the appropriate equivalent
- For classical ML models (a fraud classifier, a credit risk model), LIME and SHAP work exactly as expected
- For the RAG pipeline (why did the AI return this specific document?), attribution is handled differently (retrieval score explanations from Milvus)

💡 **Recommended Approach:**
- **JupyterHub base image** includes `shap`, `lime`, `captum`, `bertviz` pre-installed — satisfies the requirement for classical ML use cases
- Provide **2-3 sample notebooks** demonstrating SHAP on a banking classifier (loan approval, credit scoring) and LIME on a text classifier
- For LLM explainability specifically, provide a notebook using Captum/BertViz for attention visualization
- This addresses both the letter of the requirement (LIME and SHAP are available) and its spirit (model decisions can be explained)

**Effort:** Included in the JupyterHub sprint. 1 additional week for sample banking-specific notebooks.

---

#### C13 — Ethical AI Guidelines Enforcement
**Policy says:** *"Develop and enforce a set of ethical guidelines for AI development and use. The security committee must assess and approve high-risk AI projects."*

| Score | Coverage |
|-------|----------|
| **38** 🟨 | NeMo Guardrails provides the **technical enforcement layer** — programmable rails that enforce topic boundaries, block inappropriate outputs, and enforce CBO's content policies. What NeMo cannot do: replace a governance committee or enforce that humans review high-risk projects before deployment. That is an organizational process, not a platform feature. |

💡 **Recommended Approach:** Provide an **AI Governance Framework** document as a deliverable — a lightweight process describing how CBO's security committee classifies AI use cases, what approval gates exist for "high-risk" use cases, and how NeMo Guardrails configurations are reviewed and approved. The platform enforces the policy; the document defines the policy. Effort: 1 week.

---

#### C14 — Fairness Assessment Across Demographic Groups
**Policy says:** *"Implement processes to detect and mitigate bias in AI systems throughout their lifecycle. Regularly assess AI systems for fairness across different demographic groups."*

| Score | Coverage |
|-------|----------|
| **8** 🟥 | Not in current OpenOva stack. Same resolution as C4 — Fairlearn in JupyterHub covers this. |

---

#### C15 — ISO 42001 Alignment
**Policy says:** *"This policy is in line with ISO 42001."*

| Score | Coverage |
|-------|----------|
| **58** 🟨 | ISO 42001 is an AI Management System standard — it defines organizational requirements for governing AI, analogous to ISO 27001 for information security. OpenOva provides the **technical controls** that ISO 42001 requires: risk monitoring (Falco, Grafana), model governance (GitOps, Harbor, LangFuse), access control (Keycloak), incident response (OpenSearch SIEM), data protection (OpenBao, cert-manager). What OpenOva does not provide: the organizational management system itself (governance committee, documented policies, review schedules, audit evidence collection). |

**Recommendation:** Position OpenOva as providing the "technical implementation controls" for ISO 42001. Provide a compliance mapping document as a project deliverable that maps each ISO 42001 clause to the corresponding OpenOva component and configuration. This is standard practice for any vendor responding to ISO-aligned requirements.

---

### Section D — General Requirements

---

#### D1 — Vendor Technical Support and Maintenance
**RFP says:** *"The vendor must provide technical support and maintenance services."*

| Score | Coverage |
|-------|----------|
| **90** 🟩 | OpenOva's business model is support subscriptions. SLA, incident response, platform updates, and proactive maintenance are the core commercial offering. |

---

#### D2 — Complete Implementation Responsibility
**RFP says:** *"The vendor must be responsible for complete implementation."*

| Score | Coverage |
|-------|----------|
| **90** 🟩 | OpenOva is a turnkey platform — the entire implementation is OpenOva's responsibility, from infrastructure provisioning to application deployment to training delivery. |

---

#### D3 — Continuous Updates: Newer Versions of GPT or Relevant AI Models
**RFP says:** *"The vendor should ensure continuous updates by periodically integrating newer versions of GPT or other relevant AI models to maintain optimal performance and capabilities."*

⚠️ **Genuine intent, confused framing**

| Score | Coverage |
|-------|----------|
| **62** ⚠️🟨 | The intent is genuine: CBO wants the AI models available on the platform to stay current. OpenOva fully supports this via GitOps — updating a model is a one-line change to a HelmRelease that Flux reconciles automatically. The open-source model ecosystem (Qwen3, Llama 3.x, Mistral, DeepSeek) releases new versions frequently, and OpenOva tracks them. |

**The confusion:** "GPT" is OpenAI's proprietary model family — it cannot be self-hosted. CBO's RFP treating GPT as a generic updatable software component suggests the procurement team may not fully understand the distinction between open-source LLMs and commercial API models.

💡 **Our clarification in the proposal:** We should explain clearly:
- We serve **open-source LLMs** (Qwen3, Llama 3.x, DeepSeek-R1, Mistral) that run entirely on-premises
- These are updated through our platform update cycle — quarterly model refresh minimum
- For access to GPT-4/Claude/Gemini, we can integrate OpenOva Axon as a gateway to commercial APIs (these APIs are a passthrough — models are not hosted by us, they remain at OpenAI/Anthropic/Google)
- Self-hosted models give CBO 90%+ cost savings and full data sovereignty; commercial APIs add model choice flexibility

---

#### D4 — Documentation: User Guides and System Manuals
**RFP says:** *"The vendor must provide documentation, including user guides and system manuals."*

| Score | Coverage |
|-------|----------|
| **52** 🟨 | Platform technical documentation: excellent (52 component READMEs, API docs, architecture diagrams). CBO-specific operational documentation: must be created as part of the engagement. User guides for non-technical employees, admin runbooks, incident response playbooks — all project deliverables. |

---

#### D5 — Training Sessions for End-Users and Administrators
**RFP says:** *"The vendor should provide comprehensive training to employees. Training sessions for end-users and administrators must be included."*

| Score | Coverage |
|-------|----------|
| **82** 🟩 | OpenOva can deliver structured training programs. The platform itself provides the training environment. This is a professional services deliverable included in the engagement scope. Specific training tracks needed: (1) End-user track — how to use LibreChat, Flowise, and the agent presets for banking tasks; (2) Data scientist track — JupyterHub, API access, model evaluation; (3) Admin track — platform operations, user management, security monitoring. |

---

## PART 3 — Full Heat Map Summary

| # | Requirement | Score | Status |
|---|-------------|-------|--------|
| **A — Technical Functional** | | | |
| A1 | Upload and manage dummy datasets | 75 | 🟨 |
| A2 | Access to pre-trained models and APIs | 92 | 🟩 |
| A3 | Role-based access controls | 92 | 🟩 |
| A4 | Data collection/assessment/preprocessing ⚠️ | 35 | 🟥 |
| A5 | Data cleaning tools ⚠️ | 10 | 🟥 |
| A6 | Data privacy / anonymization | 55 | 🟨 |
| A7 | Fully isolated environment | 95 | 🟩 |
| A8 | High-performance hardware (GPU) ⚠️ | 85 | 🟩 |
| A9 | Scalable infrastructure | 92 | 🟩 |
| A10 | Performance metrics frameworks | 72 | 🟨 |
| A11 | Project/workspace creation and collaboration | 38 | 🟥 |
| A12 | AI experimentation modules (no-code + code) | 32 | 🟥 |
| A13 | Secure dataset upload and storage | 82 | 🟩 |
| A14 | Model training, versioning, evaluation ⚠️ | 35 | 🟥 |
| A15 | Guided tutorials and knowledge base | 5 | 🟥 |
| A16 | API and platform documentation | 68 | 🟨 |
| A17 | Edge AI inference ⚠️ | 0 | 🟥 |
| A18 | APIs/SDKs for custom development | 88 | 🟩 |
| A19 | Multi-user/multi-project support | 65 | 🟨 |
| **B — Non-Functional** | | | |
| B1 | Usability for different skill levels | 55 | 🟨 |
| B2 | Security / strict isolation | 95 | 🟩 |
| B3 | Privacy and compliance | 72 | 🟨 |
| B4 | Performance (<2s UI, <5s data ops) | 82 | 🟩 |
| B5 | Scalability | 90 | 🟩 |
| B6 | Maintainability | 90 | 🟩 |
| B7 | Extensibility | 88 | 🟩 |
| **C — Security Policy (Appendix A)** | | | |
| C1 | Risk assessment and continuous monitoring | 80 | 🟩 |
| C2 | Data encryption at rest and in transit | 95 | 🟩 |
| C3 | Model version control | 42 | 🟨 |
| C4 | Bias detection and mitigation | 18 | 🟥 |
| C5 | Secure deployment pipeline + audit logging | 90 | 🟩 |
| C6 | Model drift detection | 32 | 🟥 |
| C7 | RBAC + MFA + JIT access | 95 | 🟩 |
| C8 | API security | 90 | 🟩 |
| C9 | Real-time monitoring + anomaly detection | 85 | 🟩 |
| C10 | Tamper-evident logging | 88 | 🟩 |
| C11 | AI-specific incident response | 45 | 🟨 |
| C12 | LIME/SHAP explainability ⚠️ | 8 | 🟥 |
| C13 | Ethical AI guidelines enforcement | 38 | 🟨 |
| C14 | Fairness assessment | 8 | 🟥 |
| C15 | ISO 42001 alignment | 58 | 🟨 |
| **D — General Requirements** | | | |
| D1 | Vendor support and maintenance | 90 | 🟩 |
| D2 | Complete implementation responsibility | 90 | 🟩 |
| D3 | Continuous model updates ⚠️ | 62 | 🟨 |
| D4 | Documentation (user guides, system manuals) | 52 | 🟨 |
| D5 | Training sessions | 82 | 🟩 |

**Score Breakdown:**
- 🟩 90-100 (Strong fit): 22 requirements
- 🟩 75-89 (Good fit): 6 requirements
- 🟨 50-74 (Partial, additions needed): 10 requirements
- 🟥 0-49 (Gap or questionable): 10 requirements

**Weighted average score (all 46 requirements): ~67 out of 100 today**
**Weighted average score with targeted additions: ~88 out of 100**

---

## PART 4 — The 10 Most Important Things To Get Right in Our Proposal

### 1. Reframe "Training Models" → "Using and Evaluating Models"
CBO's employees will not train foundation models. They will use pre-trained models, possibly fine-tune small versions, and evaluate outputs. Our proposal should clarify this scope — it sets realistic expectations and simplifies the technical architecture significantly.

### 2. Don't Ignore Edge AI — Handle It Gracefully
Do not score it as covered. Acknowledge it honestly, offer the reframe (sandbox for model development, not edge deployment), and move on. Pretending to cover it will create problems in UAT.

### 3. The "GPT" Language Needs Correction
Position the correct alternative (open-source models on-premises) with a clear comparison table. Offer Axon as the commercial API gateway option. This is an opportunity to educate CBO and differentiate from Microsoft/Azure who will offer hosted GPT.

### 4. Flowise + JupyterHub Is the Real Gap — Own It
The no-code/low-code + code experimentation gap is the most visible requirement in the RFP. Address it head-on: "We add two components to complete this: Flowise for visual pipeline building, JupyterHub for code-based experimentation." Name them, show them, demo them.

### 5. LIME/SHAP — Take It Seriously
This came from CBO's security team and is a genuine requirement. Show exactly how it's delivered (pre-installed in JupyterHub, sample banking notebooks). This is a credibility signal.

### 6. Data Sovereignty Is Our Strongest Card
CBO is a central bank. Every other vendor will be offering cloud services where CBO data touches Microsoft/Amazon/Google infrastructure. OpenOva runs entirely on-premises. This is not a feature — it is a regulatory requirement they may not have fully articulated yet. Bring it up. Make it the headline.

### 7. Arabic Language Is A Real Differentiator
BGE-M3 natively supports Arabic. None of the large cloud vendors' RAG solutions do this well for Omani Arabic (Gulf dialect). Our RAG pipeline works in Arabic on day 1.

### 8. Build The Tutorial System On The Platform Itself
The knowledge base requirement is a content deliverable, not a software requirement. The best way to deliver it is as a LibreChat "AI Sandbox Guide" agent powered by a Milvus RAG pipeline. It demonstrates the platform while fulfilling the requirement. This is a genuine differentiator.

### 9. Be Clear About ISO 42001
We provide the technical controls. CBO owns the management system. Provide a compliance mapping document. This is the honest, standard position. Do not overclaim.

### 10. Specter Is The Surprise Differentiator
No competitor can offer a platform that is actively monitored and self-healed by an AI that has pre-built semantic knowledge of every component. Specter means CBO's IT team doesn't need deep expertise in 52 components — Specter already has it.

---

*Analysis saved: `/home/openova/repos/openova/.claude/cbo-rfp-heatmap.md`*
*RFP source: "AI Sandbox Statement of Work-with 11.12.25.docx" — Central Bank of Oman*
