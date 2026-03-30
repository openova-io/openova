export interface Config {
    port: number;
    apiKeys: string[];
    defaultModel: string;
    poolSize: number;
    valkeyUrl: string;
    conversationTtl: number;
}
export declare function loadConfig(): Config;
//# sourceMappingURL=config.d.ts.map