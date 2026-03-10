const b = Buffer.from('QoIDCpgCCkA3M2NkNjE5MGI5NjMyMmRmMTJjMjU1OGUzNzFiZmMyM2I4NzVjNDcxZDA5YWJhZTFhMDJlMjYxNTEyNjBmOTI5EipiYzFxOHJhMmYzMzhkamcwcHpnbXMzNXRwa2R4ang5bHIzOW1reXc4NHkiYQpfCAESWzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABA/qIZbR2Qiy88cO/hXmiUkdLoCrFrEAkw4xRRjtEjNycz+gNC7Ueihj7lnAPDDuuERVlYL12HCzLk0zjCfGN4MqQNzknopGcC1T7IYfEf5bkUZK21PWpTyfT2uaTrf/flx4N4K9l6gIN9B3FHUD5c7A/KhqJ5IUR3DFkJMCgrV/X94yATBAARIDQlRDGgMzMjEiA0JUQyoAMipiYzFxOHJhMmYzMzhkamcwcHpnbXMzNXRwa2R4ang5bHIzOW1reXc4NHk6AEIASABSIlEgQH/pg9mYG96+iP5bXsGCF3KKYt9vMMONrlpN5y4ZJK4=', 'base64');
let s = '';
for (let i = 0; i < b.length; i++) {
    const c = b[i];
    if (c >= 32 && c < 127) s += String.fromCharCode(c);
    else s += `[${c.toString(16).padStart(2, '0')}]`;
}
console.log(s);
