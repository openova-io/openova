<#import "template.ftl" as layout>
<@layout.registrationLayout displayMessage=true; section>
    <#if section = "header">
        ${msg("fapiConsentTitle")}
    <#elseif section = "form">
        <div id="fapi-consent">
            <p id="fapi-consent-request">${msg("fapiConsentRequest", fapiClient)}</p>
            <#if fapiSummary.permissions??>
                <p>${msg("fapiConsentPermissions")}</p>
                <ul id="fapi-consent-permissions">
                    <#list fapiSummary.permissions as permission>
                        <li>${permission}</li>
                    </#list>
                </ul>
            <#else>
                <p>${msg("fapiConsentPayment")}</p>
                <ul id="fapi-consent-payment">
                    <li>${fapiSummary.amount!""} ${fapiSummary.currency!""}</li>
                    <li>${fapiSummary.creditorName!""}</li>
                </ul>
            </#if>
            <#if fapiExpiresAt?has_content>
                <p id="fapi-consent-expires">${msg("fapiConsentExpires", fapiExpiresAt)}</p>
            </#if>
            <form id="fapi-consent-form" action="${url.loginAction}" method="post">
                <p>${msg("fapiConsentAccounts")}</p>
                <#list fapiAccounts as account>
                    <div>
                        <input type="checkbox" id="account-${account?index}" name="account" value="${account.id}">
                        <label for="account-${account?index}">${account.displayName} ${account.maskedNumber} ${account.currency}</label>
                    </div>
                </#list>
                <div>
                    <button type="submit" id="fapi-consent-authorise" name="decision" value="authorise">${msg("fapiConsentAuthorise")}</button>
                    <button type="submit" id="fapi-consent-reject" name="decision" value="reject">${msg("fapiConsentReject")}</button>
                </div>
            </form>
        </div>
    </#if>
</@layout.registrationLayout>
