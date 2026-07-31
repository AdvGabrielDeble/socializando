# Socializando LP — v4 Reconstruída

Esta versão foi reconstruída com foco em corrigir os problemas apontados na versão anterior:

- logo aplicado sem badge branco ou fundo colado;
- marca integrada ao layout com PNG transparente;
- hero mais limpo e mais premium;
- personagens usados de forma controlada e concentrados na seção das 5 habilidades;
- remoção do uso do Neo com bússola;
- mais respiro visual, mais hierarquia e transições mais elegantes.

## Estrutura

- `index.html` — estrutura da landing page
- `styles.css` — estilos visuais e responsivos
- `script.js` — animações suaves, scroll progress, tilt e links do WhatsApp
- `assets/` — logos, sprites e imagens

## Ajuste obrigatório antes da publicação

Abrir `script.js` e substituir:

```js
const WHATSAPP_NUMBER = "INSERIR_NUMERO_AQUI";
```

pelo número final no formato internacional, por exemplo:

```js
const WHATSAPP_NUMBER = "5553999999999";
```

## Observação

Esta v4 evita o erro de transformar o logo em “cartão colado”. A aplicação foi reconstruída para privilegiar uma leitura mais limpa e mais premium.


## Ajuste v4.1
- remoção do elemento gráfico maior do hero;
- ampliação do card informativo menor no hero;
- redução do logo do cabeçalho flutuante para melhor proporção visual.


## Ajuste v4.2
- redução adicional do logo no cabeçalho flutuante para deixá-lo mais proporcional e elegante.


## Ajuste v4.3
- cabeçalho flutuante com largura mais contida;
- logo do cabeçalho reduzido novamente;
- altura, espaçamentos e CTA do topo suavizados para um visual mais elegante.
